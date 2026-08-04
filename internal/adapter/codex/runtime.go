package codex

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/yuanshu-ai/yuanshu/internal/adapter"
	"github.com/yuanshu-ai/yuanshu/internal/adapter/codex/appserver"
	"github.com/yuanshu-ai/yuanshu/internal/adapter/codex/probe"
	"github.com/yuanshu-ai/yuanshu/internal/config"
	"github.com/yuanshu-ai/yuanshu/internal/node/store"
	"github.com/yuanshu-ai/yuanshu/internal/node/workspace"
	"github.com/yuanshu-ai/yuanshu/internal/platform"
	protocol "github.com/yuanshu-ai/yuanshu/internal/protocol/v1"
)

var (
	_ adapter.Runtime           = (*Runtime)(nil)
	_ adapter.SessionReader     = (*Runtime)(nil)
	_ adapter.SessionRunner     = (*Runtime)(nil)
	_ adapter.LiveController    = (*Runtime)(nil)
	_ adapter.RuntimeConnection = (*Runtime)(nil)
)

type pendingApproval struct {
	id          string
	requestID   appserver.RequestID
	workspaceID string
	threadID    string
	turnID      string
	itemID      string
	kind        string
	timer       *time.Timer
}

type Runtime struct {
	options      Options
	installation adapter.Installation
	events       chan adapter.AgentEvent

	opMu sync.Mutex
	mu   sync.RWMutex

	client       *appserver.Client
	generation   uint64
	loaded       map[string]uint64
	pending      map[string]*pendingApproval
	activeThread string
	activeTurn   string
	health       adapter.HealthStatus
	closed       bool
	closeOnce    sync.Once
	closeErr     error
}

func newRuntime(options Options, installation adapter.Installation) *Runtime {
	return &Runtime{
		options: options, installation: installation,
		events: make(chan adapter.AgentEvent, options.EventCapacity),
		loaded: make(map[string]uint64), pending: make(map[string]*pendingApproval),
		health: adapter.HealthStatus{State: "starting", CodexVersion: installation.Version, Protocol: installation.Protocol},
	}
}

func (r *Runtime) startClient(ctx context.Context) error {
	executable, prefix, err := resolveConfiguredCommand(r.options.Config.Binary)
	if err != nil {
		return adapter.ErrUnavailable
	}
	client, err := appserver.Start(ctx, appserver.Options{
		Processes: r.options.Processes,
		Spec: platform.ProcessSpec{
			Executable: executable,
			Args:       append(prefix, "app-server", "--stdio"),
			Env:        runtimeEnvironment(r.options.Config),
		},
	})
	if err != nil {
		return adapter.ErrUnavailable
	}
	title := "Yuanshu Codex Adapter"
	_, err = client.Initialize(ctx, appserver.ClientInfo{Name: "yuanshu", Title: &title, Version: "0.0.0"})
	if err != nil {
		_ = client.Close()
		return mapCallError(err)
	}
	var account json.RawMessage
	if err := client.Call(ctx, "account/read", map[string]any{"refreshToken": false}, &account); err != nil {
		_ = client.Close()
		return mapCallError(err)
	}
	authentication, err := classifyAuth(account)
	account = nil
	if err != nil {
		_ = client.Close()
		return adapter.ErrUnavailable
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		_ = client.Close()
		return adapter.ErrClosed
	}
	r.generation++
	generation := r.generation
	r.client = client
	r.health = adapter.HealthStatus{
		State: "ready", CodexVersion: r.installation.Version, Protocol: r.installation.Protocol,
		Authentication: string(authentication),
	}
	r.mu.Unlock()
	go r.consume(client, generation)
	r.emit(adapter.AgentEvent{Type: protocol.EventRuntimeStatus, Payload: map[string]any{"state": "ready"}})
	return nil
}

func (r *Runtime) ensureClient(ctx context.Context) (*appserver.Client, uint64, error) {
	r.mu.RLock()
	if r.closed {
		r.mu.RUnlock()
		return nil, 0, adapter.ErrClosed
	}
	client, generation := r.client, r.generation
	r.mu.RUnlock()
	if client != nil {
		select {
		case <-client.Done():
		default:
			return client, generation, nil
		}
	}
	if err := r.startClient(ctx); err != nil {
		return nil, 0, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.client, r.generation, nil
}

func (r *Runtime) ListThreads(ctx context.Context, request adapter.ListThreadsRequest) (adapter.ThreadPage, error) {
	r.opMu.Lock()
	defer r.opMu.Unlock()
	resolved, err := r.resolveWorkspace(ctx, request.WorkspaceID)
	if err != nil || request.Limit < 0 || request.Limit > 100 || !validOptionalID(request.Cursor) {
		if err != nil {
			return adapter.ThreadPage{}, err
		}
		return adapter.ThreadPage{}, adapter.ErrInvalid
	}
	limit := request.Limit
	if limit == 0 {
		limit = 20
	}
	client, _, err := r.ensureClient(ctx)
	if err != nil {
		return adapter.ThreadPage{}, err
	}
	params := map[string]any{"limit": limit, "sourceKinds": stableSourceKinds}
	if request.Cursor != "" {
		params["cursor"] = request.Cursor
	}
	var response struct {
		Data       []codexThread `json:"data"`
		NextCursor string        `json:"nextCursor"`
	}
	if err := client.Call(ctx, "thread/list", params, &response); err != nil {
		return adapter.ThreadPage{}, mapCallError(err)
	}
	page := adapter.ThreadPage{Data: make([]adapter.Thread, 0), NextCursor: response.NextCursor}
	for _, thread := range response.Data {
		if sameLocalPath(thread.Cwd, resolved.CanonicalPath) {
			page.Data = append(page.Data, publicThread(thread, request.WorkspaceID))
		}
	}
	return page, nil
}

func (r *Runtime) ReadThread(ctx context.Context, request adapter.ReadThreadRequest) (adapter.ThreadSnapshot, error) {
	r.opMu.Lock()
	defer r.opMu.Unlock()
	resolved, err := r.resolveWorkspace(ctx, request.WorkspaceID)
	if err != nil {
		return adapter.ThreadSnapshot{}, err
	}
	return r.readThread(ctx, resolved, request)
}

func (r *Runtime) readThread(ctx context.Context, resolved workspace.ResolvedWorkspace, request adapter.ReadThreadRequest) (adapter.ThreadSnapshot, error) {
	threadID := request.ThreadID
	if !validID(threadID) {
		return adapter.ThreadSnapshot{}, adapter.ErrInvalid
	}
	client, _, err := r.ensureClient(ctx)
	if err != nil {
		return adapter.ThreadSnapshot{}, err
	}
	var response struct {
		Thread codexThread `json:"thread"`
	}
	if err := client.Call(ctx, "thread/read", map[string]any{"threadId": threadID, "includeTurns": request.IncludeTurns}, &response); err != nil {
		return adapter.ThreadSnapshot{}, mapCallError(err)
	}
	if response.Thread.ID != threadID || !sameLocalPath(response.Thread.Cwd, resolved.CanonicalPath) {
		return adapter.ThreadSnapshot{}, adapter.ErrForbidden
	}
	snapshot := publicSnapshot(response.Thread, resolved.ID)
	applyReadOptions(&snapshot, request)
	return snapshot, nil
}

func (r *Runtime) StartThread(ctx context.Context, request adapter.StartThreadRequest) (adapter.Thread, error) {
	r.opMu.Lock()
	defer r.opMu.Unlock()
	resolved, err := r.resolveWorkspace(ctx, request.WorkspaceID)
	if err != nil {
		return adapter.Thread{}, err
	}
	client, generation, err := r.ensureClient(ctx)
	if err != nil {
		return adapter.Thread{}, err
	}
	var response struct {
		Thread codexThread `json:"thread"`
	}
	if err := client.Call(ctx, "thread/start", threadParams(resolved), &response); err != nil {
		return adapter.Thread{}, mapCallError(err)
	}
	if !validID(response.Thread.ID) {
		return adapter.Thread{}, adapter.ErrUnavailable
	}
	record := store.RuntimeThreadRecord{Adapter: AdapterID, ThreadID: response.Thread.ID, WorkspaceID: resolved.ID, Ownership: "created", State: store.RuntimeThreadIdle}
	if err := r.options.Threads.SaveRuntimeThread(ctx, record); err != nil {
		return adapter.Thread{}, adapter.ErrUnavailable
	}
	r.mu.Lock()
	r.loaded[record.ThreadID] = generation
	r.mu.Unlock()
	return publicThread(response.Thread, resolved.ID), nil
}

func (r *Runtime) ResumeThread(ctx context.Context, request adapter.ResumeThreadRequest) (adapter.Thread, error) {
	r.opMu.Lock()
	defer r.opMu.Unlock()
	resolved, err := r.resolveWorkspace(ctx, request.WorkspaceID)
	if err != nil {
		return adapter.Thread{}, err
	}
	snapshot, err := r.readThread(ctx, resolved, adapter.ReadThreadRequest{WorkspaceID: request.WorkspaceID, ThreadID: request.ThreadID, IncludeTurns: true, IncludeDiffs: true})
	if err != nil {
		return adapter.Thread{}, err
	}
	if snapshot.Thread.Status == "active" || hasInProgress(snapshot.Turns) {
		return adapter.Thread{}, adapter.ErrConflict
	}
	if existing, lookupErr := r.options.Threads.RuntimeThread(ctx, request.ThreadID); lookupErr == nil {
		if existing.WorkspaceID != resolved.ID {
			return adapter.Thread{}, adapter.ErrForbidden
		}
		if existing.State == store.RuntimeThreadNeedsReconcile || existing.State == store.RuntimeThreadStarting || existing.State == store.RuntimeThreadActive {
			return adapter.Thread{}, adapter.ErrReconciliationNeeded
		}
	} else if !errors.Is(lookupErr, store.ErrNotFound) {
		return adapter.Thread{}, adapter.ErrUnavailable
	}
	client, generation, err := r.ensureClient(ctx)
	if err != nil {
		return adapter.Thread{}, err
	}
	var response struct {
		Thread codexThread `json:"thread"`
	}
	params := threadParams(resolved)
	params["threadId"] = request.ThreadID
	if err := client.Call(ctx, "thread/resume", params, &response); err != nil {
		return adapter.Thread{}, mapCallError(err)
	}
	if response.Thread.ID != request.ThreadID {
		return adapter.Thread{}, adapter.ErrUnavailable
	}
	record := store.RuntimeThreadRecord{Adapter: AdapterID, ThreadID: request.ThreadID, WorkspaceID: resolved.ID, Ownership: "resumed", State: store.RuntimeThreadIdle}
	if err := r.options.Threads.SaveRuntimeThread(ctx, record); err != nil {
		return adapter.Thread{}, adapter.ErrUnavailable
	}
	r.mu.Lock()
	r.loaded[record.ThreadID] = generation
	r.mu.Unlock()
	return publicThread(response.Thread, resolved.ID), nil
}

func (r *Runtime) StartTurn(ctx context.Context, request adapter.StartTurnRequest) (adapter.Turn, error) {
	r.opMu.Lock()
	defer r.opMu.Unlock()
	if !validInput(request.Input) {
		return adapter.Turn{}, adapter.ErrInvalid
	}
	resolved, record, err := r.ownedThread(ctx, request.WorkspaceID, request.ThreadID)
	if err != nil {
		return adapter.Turn{}, err
	}
	if record.State != store.RuntimeThreadIdle || r.anyUncertainThread(ctx) {
		return adapter.Turn{}, adapter.ErrConflict
	}
	client, generation, err := r.ensureClient(ctx)
	if err != nil {
		return adapter.Turn{}, err
	}
	if err := r.ensureLoaded(ctx, client, generation, resolved, record); err != nil {
		return adapter.Turn{}, err
	}
	record.State = store.RuntimeThreadStarting
	record.ActiveTurnID = ""
	if err := r.options.Threads.SaveRuntimeThread(ctx, record); err != nil {
		return adapter.Turn{}, adapter.ErrUnavailable
	}
	r.mu.Lock()
	r.activeThread = record.ThreadID
	r.activeTurn = ""
	r.mu.Unlock()
	var response struct {
		Turn codexTurn `json:"turn"`
	}
	params := map[string]any{
		"threadId":       record.ThreadID,
		"cwd":            resolved.CanonicalPath,
		"approvalPolicy": "on-request",
		"sandboxPolicy":  sandboxPolicy(resolved),
		"input":          []map[string]string{{"type": "text", "text": request.Input}},
	}
	if callErr := client.Call(ctx, "turn/start", params, &response); callErr != nil {
		mapped := mapCallError(callErr)
		if errors.Is(mapped, adapter.ErrAmbiguous) {
			record.State = store.RuntimeThreadNeedsReconcile
		} else {
			record.State = store.RuntimeThreadIdle
		}
		_ = r.options.Threads.SaveRuntimeThread(context.Background(), record)
		r.clearActive(record.ThreadID, "")
		return adapter.Turn{}, mapped
	}
	if !validID(response.Turn.ID) {
		record.State = store.RuntimeThreadNeedsReconcile
		_ = r.options.Threads.SaveRuntimeThread(context.Background(), record)
		return adapter.Turn{}, adapter.ErrAmbiguous
	}
	record.State, record.ActiveTurnID = store.RuntimeThreadActive, response.Turn.ID
	if err := r.options.Threads.SaveRuntimeThread(ctx, record); err != nil {
		return adapter.Turn{}, adapter.ErrAmbiguous
	}
	r.mu.Lock()
	r.activeTurn = response.Turn.ID
	r.mu.Unlock()
	return adapter.Turn{ID: response.Turn.ID, ThreadID: record.ThreadID, Status: response.Turn.Status}, nil
}

func (r *Runtime) SteerTurn(ctx context.Context, request adapter.SteerTurnRequest) error {
	if !validInput(request.Input) {
		return adapter.ErrInvalid
	}
	return r.activeCall(ctx, request.WorkspaceID, request.ThreadID, request.TurnID, "turn/steer", map[string]any{
		"threadId": request.ThreadID, "expectedTurnId": request.TurnID,
		"input": []map[string]string{{"type": "text", "text": request.Input}},
	})
}

func (r *Runtime) InterruptTurn(ctx context.Context, request adapter.InterruptTurnRequest) error {
	return r.activeCall(ctx, request.WorkspaceID, request.ThreadID, request.TurnID, "turn/interrupt", map[string]string{"threadId": request.ThreadID, "turnId": request.TurnID})
}

func (r *Runtime) activeCall(ctx context.Context, workspaceID, threadID, turnID, method string, params any) error {
	r.opMu.Lock()
	defer r.opMu.Unlock()
	if !validID(turnID) {
		return adapter.ErrInvalid
	}
	_, record, err := r.ownedThread(ctx, workspaceID, threadID)
	if err != nil {
		return err
	}
	if record.State != store.RuntimeThreadActive || record.ActiveTurnID != turnID {
		return adapter.ErrConflict
	}
	client, _, err := r.ensureClient(ctx)
	if err != nil {
		return err
	}
	if err := client.Call(ctx, method, params, nil); err != nil {
		return mapCallError(err)
	}
	return nil
}

func (r *Runtime) ResolveApproval(ctx context.Context, decision adapter.ApprovalDecision) error {
	r.opMu.Lock()
	defer r.opMu.Unlock()
	if decision.Decision != "accept" && decision.Decision != "decline" {
		return adapter.ErrInvalid
	}
	r.mu.Lock()
	pending, exists := r.pending[decision.ApprovalID]
	if exists && pending.workspaceID == decision.WorkspaceID && pending.threadID == decision.ThreadID && pending.turnID == decision.TurnID && pending.itemID == decision.ItemID {
		delete(r.pending, decision.ApprovalID)
		pending.timer.Stop()
	} else {
		exists = false
	}
	client := r.client
	r.mu.Unlock()
	if !exists {
		return adapter.ErrConflict
	}
	if _, _, err := r.ownedThread(ctx, decision.WorkspaceID, decision.ThreadID); err != nil {
		return err
	}
	if client == nil {
		return adapter.ErrAmbiguous
	}
	if err := client.Respond(pending.requestID, map[string]string{"decision": decision.Decision}, nil); err != nil {
		return mapCallError(err)
	}
	r.emit(adapter.AgentEvent{Type: protocol.EventApprovalResolved, WorkspaceID: pending.workspaceID, ThreadID: pending.threadID, TurnID: pending.turnID, ItemID: pending.itemID, Payload: map[string]any{"approvalId": pending.id, "decision": decision.Decision}})
	return nil
}

func (r *Runtime) Events() <-chan adapter.AgentEvent { return r.events }

func (r *Runtime) Health() adapter.HealthStatus {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.health
}

func (r *Runtime) Close(ctx context.Context) error {
	r.closeOnce.Do(func() {
		if ctx == nil {
			r.closeErr = context.Canceled
			return
		}
		r.opMu.Lock()
		defer r.opMu.Unlock()
		r.mu.Lock()
		r.closed = true
		client := r.client
		pending := make([]*pendingApproval, 0, len(r.pending))
		for _, item := range r.pending {
			item.timer.Stop()
			pending = append(pending, item)
		}
		r.pending = make(map[string]*pendingApproval)
		activeThread, activeTurn := r.activeThread, r.activeTurn
		r.mu.Unlock()
		if client != nil {
			for _, item := range pending {
				_ = client.Respond(item.requestID, map[string]string{"decision": "decline"}, nil)
			}
			if activeThread != "" && activeTurn != "" {
				_ = client.Call(ctx, "turn/interrupt", map[string]string{"threadId": activeThread, "turnId": activeTurn}, nil)
			}
			r.closeErr = client.Close()
		}
		if activeThread != "" {
			if record, err := r.options.Threads.RuntimeThread(context.Background(), activeThread); err == nil && record.State != store.RuntimeThreadIdle {
				record.State = store.RuntimeThreadNeedsReconcile
				_ = r.options.Threads.SaveRuntimeThread(context.Background(), record)
			}
		}
		r.mu.Lock()
		r.health.State = "closed"
		r.mu.Unlock()
		close(r.events)
	})
	return r.closeErr
}

func (r *Runtime) resolveWorkspace(ctx context.Context, id string) (workspace.ResolvedWorkspace, error) {
	if !validID(id) {
		return workspace.ResolvedWorkspace{}, adapter.ErrInvalid
	}
	resolved, err := r.options.Workspaces.Resolve(ctx, id)
	if err != nil {
		switch {
		case errors.Is(err, workspace.ErrNotFound):
			return workspace.ResolvedWorkspace{}, adapter.ErrNotFound
		case errors.Is(err, workspace.ErrDenied), errors.Is(err, workspace.ErrStale):
			return workspace.ResolvedWorkspace{}, adapter.ErrForbidden
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			return workspace.ResolvedWorkspace{}, err
		default:
			return workspace.ResolvedWorkspace{}, adapter.ErrUnavailable
		}
	}
	if resolved.Adapter != AdapterID {
		return workspace.ResolvedWorkspace{}, adapter.ErrForbidden
	}
	return resolved, nil
}

func (r *Runtime) ownedThread(ctx context.Context, workspaceID, threadID string) (workspace.ResolvedWorkspace, store.RuntimeThreadRecord, error) {
	resolved, err := r.resolveWorkspace(ctx, workspaceID)
	if err != nil {
		return workspace.ResolvedWorkspace{}, store.RuntimeThreadRecord{}, err
	}
	if !validID(threadID) {
		return workspace.ResolvedWorkspace{}, store.RuntimeThreadRecord{}, adapter.ErrInvalid
	}
	record, err := r.options.Threads.RuntimeThread(ctx, threadID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return workspace.ResolvedWorkspace{}, store.RuntimeThreadRecord{}, adapter.ErrNotFound
		}
		return workspace.ResolvedWorkspace{}, store.RuntimeThreadRecord{}, adapter.ErrUnavailable
	}
	if record.Adapter != AdapterID || record.WorkspaceID != workspaceID {
		return workspace.ResolvedWorkspace{}, store.RuntimeThreadRecord{}, adapter.ErrForbidden
	}
	if record.State == store.RuntimeThreadNeedsReconcile {
		return workspace.ResolvedWorkspace{}, store.RuntimeThreadRecord{}, adapter.ErrReconciliationNeeded
	}
	return resolved, record, nil
}

func (r *Runtime) anyUncertainThread(ctx context.Context) bool {
	records, err := r.options.Threads.RuntimeThreads(ctx)
	if err != nil {
		return true
	}
	for _, record := range records {
		if record.State != store.RuntimeThreadIdle {
			return true
		}
	}
	return false
}

func (r *Runtime) ensureLoaded(ctx context.Context, client *appserver.Client, generation uint64, resolved workspace.ResolvedWorkspace, record store.RuntimeThreadRecord) error {
	r.mu.RLock()
	loaded := r.loaded[record.ThreadID] == generation
	r.mu.RUnlock()
	if loaded {
		return nil
	}
	var response struct {
		Thread codexThread `json:"thread"`
	}
	params := threadParams(resolved)
	params["threadId"] = record.ThreadID
	if err := client.Call(ctx, "thread/resume", params, &response); err != nil {
		return mapCallError(err)
	}
	if response.Thread.ID != record.ThreadID || !sameLocalPath(response.Thread.Cwd, resolved.CanonicalPath) {
		return adapter.ErrForbidden
	}
	r.mu.Lock()
	r.loaded[record.ThreadID] = generation
	r.mu.Unlock()
	return nil
}

func (r *Runtime) clearActive(threadID, turnID string) {
	r.mu.Lock()
	if r.activeThread == threadID && (turnID == "" || r.activeTurn == turnID) {
		r.activeThread, r.activeTurn = "", ""
	}
	r.mu.Unlock()
}

func (r *Runtime) emit(event adapter.AgentEvent) {
	r.mu.RLock()
	if r.closed {
		r.mu.RUnlock()
		return
	}
	client := r.client
	overflow := false
	select {
	case r.events <- event:
	default:
		overflow = true
	}
	r.mu.RUnlock()
	if !overflow {
		return
	}
	r.mu.Lock()
	r.health.State, r.health.FailureCode = "failed", "event_backpressure"
	r.mu.Unlock()
	if client != nil {
		go client.Close()
	}
}

func threadParams(resolved workspace.ResolvedWorkspace) map[string]any {
	sandbox := "read-only"
	if resolved.PermissionProfile == config.PermissionWorkspaceWrite {
		sandbox = "workspace-write"
	}
	return map[string]any{"cwd": resolved.CanonicalPath, "approvalPolicy": "on-request", "sandbox": sandbox, "serviceName": "yuanshu"}
}

func sandboxPolicy(resolved workspace.ResolvedWorkspace) map[string]any {
	if resolved.PermissionProfile == config.PermissionWorkspaceWrite {
		return map[string]any{"type": "workspaceWrite", "networkAccess": resolved.AllowNetwork, "writableRoots": []string{}}
	}
	return map[string]any{"type": "readOnly", "networkAccess": resolved.AllowNetwork}
}

func validID(value string) bool         { return validText(value, 128) }
func validOptionalID(value string) bool { return value == "" || validID(value) }
func validInput(value string) bool      { return validText(value, 256<<10) }
func validText(value string, max int) bool {
	if !utf8.ValidString(value) || strings.TrimSpace(value) == "" || len(value) > max {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) && character != '\n' && character != '\t' && character != '\r' {
			return false
		}
	}
	return true
}

func sameLocalPath(left, right string) bool {
	return strings.EqualFold(filepath.Clean(left), filepath.Clean(right))
}

var stableSourceKinds = []string{"cli", "vscode", "exec", "appServer", "subAgent", "subAgentReview", "subAgentCompact", "subAgentThreadSpawn", "subAgentOther", "unknown"}

type codexThread struct {
	ID        string  `json:"id"`
	Cwd       string  `json:"cwd"`
	Name      *string `json:"name"`
	Preview   string  `json:"preview"`
	CreatedAt int64   `json:"createdAt"`
	UpdatedAt int64   `json:"updatedAt"`
	Status    struct {
		Type string `json:"type"`
	} `json:"status"`
	Turns []codexTurn `json:"turns"`
}

type codexTurn struct {
	ID        string            `json:"id"`
	Status    string            `json:"status"`
	Items     []codexThreadItem `json:"items"`
	ItemsView string            `json:"itemsView"`
	Error     *codexTurnError   `json:"error"`
}

type codexTurnError struct {
	Message           string `json:"message"`
	AdditionalDetails string `json:"additionalDetails"`
}

type codexThreadItem struct {
	ID               string            `json:"id"`
	Type             string            `json:"type"`
	Status           string            `json:"status"`
	Text             string            `json:"text"`
	Command          string            `json:"command"`
	AggregatedOutput string            `json:"aggregatedOutput"`
	ExitCode         *int              `json:"exitCode"`
	Tool             string            `json:"tool"`
	Server           string            `json:"server"`
	Content          []json.RawMessage `json:"content"`
	Changes          []codexFileChange `json:"changes"`
	Error            *codexItemError   `json:"error"`
}

type codexFileChange struct {
	Path string `json:"path"`
	Kind string `json:"kind"`
	Diff string `json:"diff"`
}

type codexItemError struct {
	Message string `json:"message"`
}

func publicThread(value codexThread, workspaceID string) adapter.Thread {
	title := ""
	if value.Name != nil {
		title = boundedHistoryText(*value.Name)
	}
	return adapter.Thread{
		ID: value.ID, WorkspaceID: workspaceID, Status: value.Status.Type,
		Title: title, Preview: boundedHistoryText(value.Preview),
		HistoryState: "partial", CreatedAt: time.Unix(value.CreatedAt, 0).UTC(), UpdatedAt: time.Unix(value.UpdatedAt, 0).UTC(),
	}
}

func publicSnapshot(value codexThread, workspaceID string) adapter.ThreadSnapshot {
	snapshot := adapter.ThreadSnapshot{Thread: publicThread(value, workspaceID), Turns: make([]adapter.Turn, len(value.Turns))}
	complete := true
	for index, turn := range value.Turns {
		items := make([]adapter.ThreadItem, 0, len(turn.Items)+1)
		for _, item := range turn.Items {
			mapped, ok := publicThreadItem(item, value.Cwd)
			if !ok {
				complete = false
				continue
			}
			if mapped.Partial {
				complete = false
			}
			items = append(items, mapped)
			if item.Type == "fileChange" {
				for changeIndex, change := range item.Changes[1:] {
					extra := publicFileChangeItem(fmt.Sprintf("%s:%d", item.ID, changeIndex+1), item.Status, change, value.Cwd)
					if extra.Partial {
						complete = false
					}
					items = append(items, extra)
				}
			}
		}
		if turn.Error != nil && strings.TrimSpace(turn.Error.Message) != "" {
			complete = false
			items = append(items, adapter.ThreadItem{ID: turn.ID + ":error", Kind: "error", Status: "failed", ErrorMessage: boundedHistoryText(turn.Error.Message), Partial: true})
		}
		historyState := "complete"
		if turn.ItemsView != "" && turn.ItemsView != "full" {
			historyState = "partial"
			complete = false
		}
		snapshot.Turns[index] = adapter.Turn{ID: turn.ID, ThreadID: value.ID, Status: turn.Status, HistoryState: historyState, Items: items}
	}
	if len(value.Turns) == 0 {
		snapshot.Thread.HistoryState = "unavailable"
	} else if !complete {
		snapshot.Thread.HistoryState = "partial"
	} else {
		snapshot.Thread.HistoryState = "complete"
	}
	return snapshot
}

func publicThreadItem(value codexThreadItem, root string) (adapter.ThreadItem, bool) {
	if value.ID == "" {
		return adapter.ThreadItem{}, false
	}
	item := adapter.ThreadItem{ID: value.ID, Status: value.Status}
	switch value.Type {
	case "userMessage":
		item.Kind, item.Text = "user_message", boundedHistoryTextFlag(contentText(value.Content), &item.Truncated)
		if item.Text == "" {
			item.Text = boundedHistoryTextFlag(value.Text, &item.Truncated)
		}
	case "agentMessage":
		item.Kind, item.Text = "agent_message", boundedHistoryTextFlag(value.Text, &item.Truncated)
	case "commandExecution":
		item.Kind = "command"
		item.Command = boundedHistoryTextFlag(redactHistory(value.Command, root), &item.Truncated)
		item.Output = boundedHistoryTextFlag(redactHistory(value.AggregatedOutput, root), &item.Truncated)
		item.ExitCode = value.ExitCode
	case "fileChange":
		item.Kind = "file_change"
		if len(value.Changes) == 0 {
			item.Partial = true
			return item, true
		}
		item = publicFileChangeItem(value.ID, value.Status, value.Changes[0], root)
	case "mcpToolCall", "dynamicToolCall", "collabAgentToolCall", "webSearch":
		item.Kind, item.ToolName = "tool", boundedHistoryTextFlag(value.Tool, &item.Truncated)
		if item.ToolName == "" {
			item.ToolName = boundedHistoryTextFlag(value.Server, &item.Truncated)
		}
	case "plan", "reasoning", "hookPrompt":
		item.Kind, item.Text, item.Partial = "unknown", boundedHistoryTextFlag(value.Text, &item.Truncated), true
	default:
		item.Kind, item.Partial = "unknown", true
	}
	if value.Error != nil && value.Error.Message != "" {
		item.ErrorMessage, item.Partial = boundedHistoryTextFlag(value.Error.Message, &item.Truncated), true
	}
	return item, true
}

func publicFileChangeItem(id, status string, change codexFileChange, root string) adapter.ThreadItem {
	item := adapter.ThreadItem{ID: id, Kind: "file_change", Status: status}
	logical, valid := logicalExistingOrFuture(root, change.Path)
	if !valid {
		item.Partial = true
		return item
	}
	item.Path = logical
	item.ChangeType = normalizeChangeKind(change.Kind)
	redacted := redactHistory(change.Diff, root)
	item.Diff, item.Truncated, item.DiffTotalBytes, item.DiffDigest = boundedDiff(redacted)
	return item
}

func applyReadOptions(snapshot *adapter.ThreadSnapshot, request adapter.ReadThreadRequest) {
	maxBytes := request.MaxDiffBytes
	if maxBytes <= 0 || maxBytes > 64<<10 {
		maxBytes = 64 << 10
	}
	for turnIndex := range snapshot.Turns {
		for itemIndex := range snapshot.Turns[turnIndex].Items {
			item := &snapshot.Turns[turnIndex].Items[itemIndex]
			if item.Kind != "file_change" && item.Kind != "diff" {
				continue
			}
			if request.DiffPath != "" && item.Path != request.DiffPath {
				item.Diff = ""
				continue
			}
			if !request.IncludeDiffs {
				item.Diff = ""
				continue
			}
			if len(item.Diff) > maxBytes {
				item.Diff = truncateBytesUTF8(item.Diff, maxBytes)
				item.Truncated = true
			}
		}
	}
}

func boundedDiff(value string) (string, bool, int, string) {
	bytesValue := []byte(value)
	digest := sha256.Sum256(bytesValue)
	bounded, truncated := boundedHistory(value)
	return bounded, truncated, len(bytesValue), base64.RawURLEncoding.EncodeToString(digest[:])
}

func truncateBytesUTF8(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	for limit > 0 && !utf8.ValidString(value[:limit]) {
		limit--
	}
	return value[:limit]
}

func contentText(content []json.RawMessage) string {
	parts := make([]string, 0, len(content))
	for _, raw := range content {
		var value struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(raw, &value) == nil && value.Text != "" {
			parts = append(parts, value.Text)
			continue
		}
		var text string
		if json.Unmarshal(raw, &text) == nil && text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n")
}

func normalizeChangeKind(value string) string {
	switch value {
	case "add", "create", "created":
		return "created"
	case "delete", "deleted":
		return "deleted"
	case "rename", "renamed":
		return "renamed"
	default:
		return "modified"
	}
}

func redactHistory(value, root string) string {
	return probe.RedactText(redactWorkspace(value, root))
}

func boundedHistoryText(value string) string {
	bounded, _ := boundedHistory(value)
	return bounded
}

func boundedHistoryTextFlag(value string, truncated *bool) string {
	bounded, wasTruncated := boundedHistory(value)
	if wasTruncated && truncated != nil {
		*truncated = true
	}
	return bounded
}

func boundedHistory(value string) (string, bool) {
	limit := 64 << 10
	value = probe.RedactText(value)
	if len(value) <= limit {
		return value, false
	}
	for len(value) > limit && !utf8.ValidString(value[:limit]) {
		limit--
	}
	return value[:limit], true
}

func hasInProgress(turns []adapter.Turn) bool {
	for _, turn := range turns {
		if turn.Status == "inProgress" {
			return true
		}
	}
	return false
}
