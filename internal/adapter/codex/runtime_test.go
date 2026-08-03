package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sync"
	"testing"
	"time"

	adapterpkg "github.com/yuanshu-ai/yuanshu/internal/adapter"
	"github.com/yuanshu-ai/yuanshu/internal/config"
	"github.com/yuanshu-ai/yuanshu/internal/node/store"
	"github.com/yuanshu-ai/yuanshu/internal/node/workspace"
	"github.com/yuanshu-ai/yuanshu/internal/platform"
	"github.com/yuanshu-ai/yuanshu/internal/platform/fake"
	protocol "github.com/yuanshu-ai/yuanshu/internal/protocol/v1"
)

func TestFormalAdapterThreadTurnApprovalAndEvents(t *testing.T) {
	root := filepath.Join(t.TempDir(), "workspace")
	workspaces := &fakeWorkspaceResolver{resolved: workspace.ResolvedWorkspace{Descriptor: workspace.Descriptor{
		ID: "workspace-1", Adapter: "codex", PermissionProfile: config.PermissionWorkspaceWrite,
	}, CanonicalPath: root, FilesystemRoot: filepath.VolumeName(root) + string(filepath.Separator), FileIdentity: "synthetic-file-id"}}
	threads := newMemoryThreadStore()
	server := &syntheticServer{workspace: root}
	processes := newScriptedProcessManager(server.serve)
	formal, err := New(Options{
		Config:    config.CodexAdapterConfig{Enabled: true, Binary: "synthetic-codex", RuntimeMode: "stdio"},
		Processes: processes, Workspaces: workspaces, Threads: threads, ApprovalTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	installation, err := formal.Detect(context.Background())
	if err != nil || installation.Version != BaselineVersion {
		t.Fatalf("Detect = %#v, %v", installation, err)
	}
	runtimeValue, err := formal.StartRuntime(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	runtime := runtimeValue.(*Runtime)
	defer runtime.Close(context.Background())

	thread, err := runtime.StartThread(context.Background(), adapterpkg.StartThreadRequest{WorkspaceID: "workspace-1"})
	if err != nil || thread.ID != "thread-1" {
		t.Fatalf("StartThread = %#v, %v", thread, err)
	}
	page, err := runtime.ListThreads(context.Background(), adapterpkg.ListThreadsRequest{WorkspaceID: "workspace-1"})
	if err != nil || len(page.Data) != 1 || page.Data[0].ID != thread.ID {
		t.Fatalf("ListThreads = %#v, %v", page, err)
	}
	turn, err := runtime.StartTurn(context.Background(), adapterpkg.StartTurnRequest{WorkspaceID: "workspace-1", ThreadID: thread.ID, Input: "synthetic private prompt"})
	if err != nil || turn.ID != "turn-1" {
		t.Fatalf("StartTurn = %#v, %v", turn, err)
	}

	approval := waitForEvent(t, runtime.Events(), protocol.EventApprovalRequested)
	if approval.Approval == nil || approval.Approval.Kind != "command" || approval.WorkspaceID != "workspace-1" {
		t.Fatalf("approval event = %#v", approval)
	}
	if err := runtime.ResolveApproval(context.Background(), adapterpkg.ApprovalDecision{
		WorkspaceID: "workspace-1", ThreadID: thread.ID, TurnID: turn.ID, ItemID: "item-1",
		ApprovalID: approval.Approval.ID, Decision: "decline",
	}); err != nil {
		t.Fatal(err)
	}
	completed := waitForEvent(t, runtime.Events(), protocol.EventTurnCompleted)
	if completed.TurnID != turn.ID {
		t.Fatalf("completed event = %#v", completed)
	}
	record, err := threads.RuntimeThread(context.Background(), thread.ID)
	if err != nil || record.State != store.RuntimeThreadIdle || record.ActiveTurnID != "" {
		t.Fatalf("thread record = %#v, %v", record, err)
	}
	sawWorkspaceWrite, sawCwd, approvalDecision := server.observations()
	if !sawWorkspaceWrite || sawCwd != root || approvalDecision != "decline" {
		t.Fatalf("server observations = %+v", server)
	}
}

func TestPublicSnapshotMapsStableHistoryItems(t *testing.T) {
	root := filepath.Join(t.TempDir(), "workspace")
	exitCode := 0
	snapshot := publicSnapshot(codexThread{
		ID: "thread-history", Cwd: root, Preview: "ship the feature", CreatedAt: 1, UpdatedAt: 2,
		Status: struct {
			Type string `json:"type"`
		}{Type: "idle"},
		Turns: []codexTurn{{
			ID: "turn-history", Status: "completed", ItemsView: "full",
			Items: []codexThreadItem{
				{ID: "user", Type: "userMessage", Content: []json.RawMessage{json.RawMessage(`{"type":"text","text":"ship it"}`)}},
				{ID: "agent", Type: "agentMessage", Text: "done"},
				{ID: "command", Type: "commandExecution", Status: "completed", Command: "go test", AggregatedOutput: "ok", ExitCode: &exitCode},
				{ID: "file", Type: "fileChange", Status: "completed", Changes: []codexFileChange{{Path: filepath.Join(root, "internal", "app.go"), Kind: "add", Diff: "+new"}}},
				{ID: "tool", Type: "mcpToolCall", Status: "completed", Tool: "search"},
				{ID: "unknown", Type: "futureItem", Status: "completed"},
			},
		}},
	}, "workspace")
	if snapshot.Thread.HistoryState != "partial" || snapshot.Thread.Preview != "ship the feature" {
		t.Fatalf("snapshot metadata = %#v", snapshot.Thread)
	}
	if len(snapshot.Turns) != 1 || len(snapshot.Turns[0].Items) != 6 {
		t.Fatalf("snapshot items = %#v", snapshot.Turns)
	}
	items := snapshot.Turns[0].Items
	if items[0].Kind != "user_message" || items[0].Text != "ship it" || items[2].Output != "ok" || items[3].Path != "internal/app.go" || items[4].ToolName != "search" || !items[5].Partial {
		t.Fatalf("mapped items = %#v", items)
	}
}

func TestFormalAdapterStartsRuntimeForSecondCompatibilityProfile(t *testing.T) {
	server := &syntheticServer{version: "0.146.0-alpha.9.2"}
	formal, err := New(Options{
		Config:    config.CodexAdapterConfig{Enabled: true, Binary: "synthetic-codex", RuntimeMode: "stdio"},
		Processes: newScriptedProcessManager(server.serve), Workspaces: &fakeWorkspaceResolver{}, Threads: newMemoryThreadStore(),
	})
	if err != nil {
		t.Fatal(err)
	}
	runtimeValue, err := formal.StartRuntime(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	runtime := runtimeValue.(*Runtime)
	defer runtime.Close(context.Background())
	if health := runtime.Health(); health.State != "ready" || health.CodexVersion != "0.146.0-alpha.9.2" {
		t.Fatalf("runtime health = %#v", health)
	}
}

func TestRuntimeCrashMarksActiveTurnForReconciliation(t *testing.T) {
	root := t.TempDir()
	workspaces := &fakeWorkspaceResolver{resolved: workspace.ResolvedWorkspace{Descriptor: workspace.Descriptor{
		ID: "workspace-1", Adapter: "codex", PermissionProfile: config.PermissionReadOnly,
	}, CanonicalPath: root, FilesystemRoot: filepath.VolumeName(root) + string(filepath.Separator), FileIdentity: "synthetic-file-id"}}
	threads := newMemoryThreadStore()
	server := &syntheticServer{workspace: root, crashOnTurnStart: true}
	formal, err := New(Options{
		Config:    config.CodexAdapterConfig{Enabled: true, Binary: "synthetic-codex", RuntimeMode: "stdio"},
		Processes: newScriptedProcessManager(server.serve), Workspaces: workspaces, Threads: threads,
	})
	if err != nil {
		t.Fatal(err)
	}
	runtimeValue, err := formal.StartRuntime(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	runtime := runtimeValue.(*Runtime)
	thread, err := runtime.StartThread(context.Background(), adapterpkg.StartThreadRequest{WorkspaceID: "workspace-1"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runtime.StartTurn(context.Background(), adapterpkg.StartTurnRequest{WorkspaceID: "workspace-1", ThreadID: thread.ID, Input: "synthetic prompt"})
	if !errors.Is(err, adapterpkg.ErrAmbiguous) {
		t.Fatalf("StartTurn crash error = %v", err)
	}
	record, readErr := threads.RuntimeThread(context.Background(), thread.ID)
	if readErr != nil || record.State != store.RuntimeThreadNeedsReconcile {
		t.Fatalf("thread record = %#v, %v", record, readErr)
	}
	if _, err := runtime.StartTurn(context.Background(), adapterpkg.StartTurnRequest{WorkspaceID: "workspace-1", ThreadID: thread.ID, Input: "retry"}); !errors.Is(err, adapterpkg.ErrReconciliationNeeded) {
		t.Fatalf("retry error = %v", err)
	}
	_ = runtime.Close(context.Background())
}

func TestFileApprovalMappingAndReadOnlyFailClosed(t *testing.T) {
	for _, test := range []struct {
		name       string
		permission config.PermissionProfile
		wantEvent  bool
	}{
		{"workspace write exposes one-shot approval", config.PermissionWorkspaceWrite, true},
		{"read only declines locally", config.PermissionReadOnly, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			workspaces := &fakeWorkspaceResolver{resolved: workspace.ResolvedWorkspace{Descriptor: workspace.Descriptor{
				ID: "workspace-1", Adapter: "codex", PermissionProfile: test.permission,
			}, CanonicalPath: root, FilesystemRoot: filepath.VolumeName(root) + string(filepath.Separator), FileIdentity: "synthetic-file-id"}}
			threads := newMemoryThreadStore()
			server := &syntheticServer{workspace: root, approvalMethod: "file"}
			formal, err := New(Options{
				Config:    config.CodexAdapterConfig{Enabled: true, Binary: "synthetic-codex", RuntimeMode: "stdio"},
				Processes: newScriptedProcessManager(server.serve), Workspaces: workspaces, Threads: threads,
			})
			if err != nil {
				t.Fatal(err)
			}
			runtimeValue, err := formal.StartRuntime(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			runtime := runtimeValue.(*Runtime)
			defer runtime.Close(context.Background())
			thread, err := runtime.StartThread(context.Background(), adapterpkg.StartThreadRequest{WorkspaceID: "workspace-1"})
			if err != nil {
				t.Fatal(err)
			}
			turn, err := runtime.StartTurn(context.Background(), adapterpkg.StartTurnRequest{WorkspaceID: "workspace-1", ThreadID: thread.ID, Input: "synthetic prompt"})
			if err != nil {
				t.Fatal(err)
			}
			if test.wantEvent {
				event := waitForEvent(t, runtime.Events(), protocol.EventApprovalRequested)
				if event.Approval == nil || event.Approval.Kind != "file-change" {
					t.Fatalf("file approval event = %#v", event)
				}
				if err := runtime.ResolveApproval(context.Background(), adapterpkg.ApprovalDecision{WorkspaceID: "workspace-1", ThreadID: thread.ID, TurnID: turn.ID, ItemID: "item-1", ApprovalID: event.Approval.ID, Decision: "decline"}); err != nil {
					t.Fatal(err)
				}
			} else {
				terminal := waitForEvent(t, runtime.Events(), protocol.EventTurnCompleted)
				_, _, decision := server.observations()
				if terminal.TurnID != turn.ID || decision != "decline" {
					t.Fatalf("local decline terminal=%#v decision=%q", terminal, decision)
				}
			}
		})
	}
}

func TestApprovalExpiresAndCannotBeResolvedTwice(t *testing.T) {
	root := t.TempDir()
	workspaces := &fakeWorkspaceResolver{resolved: workspace.ResolvedWorkspace{Descriptor: workspace.Descriptor{
		ID: "workspace-1", Adapter: "codex", PermissionProfile: config.PermissionWorkspaceWrite,
	}, CanonicalPath: root, FilesystemRoot: filepath.VolumeName(root) + string(filepath.Separator), FileIdentity: "synthetic-file-id"}}
	threads := newMemoryThreadStore()
	server := &syntheticServer{workspace: root}
	formal, err := New(Options{
		Config:    config.CodexAdapterConfig{Enabled: true, Binary: "synthetic-codex", RuntimeMode: "stdio"},
		Processes: newScriptedProcessManager(server.serve), Workspaces: workspaces, Threads: threads,
		ApprovalTimeout: 20 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	runtimeValue, err := formal.StartRuntime(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	runtime := runtimeValue.(*Runtime)
	defer runtime.Close(context.Background())
	thread, err := runtime.StartThread(context.Background(), adapterpkg.StartThreadRequest{WorkspaceID: "workspace-1"})
	if err != nil {
		t.Fatal(err)
	}
	turn, err := runtime.StartTurn(context.Background(), adapterpkg.StartTurnRequest{WorkspaceID: "workspace-1", ThreadID: thread.ID, Input: "synthetic prompt"})
	if err != nil {
		t.Fatal(err)
	}
	requested := waitForEvent(t, runtime.Events(), protocol.EventApprovalRequested)
	resolved := waitForEvent(t, runtime.Events(), protocol.EventApprovalResolved)
	decision := server.waitDecision(time.Second)
	if resolved.TurnID != turn.ID || decision != "decline" {
		t.Fatalf("expired approval event=%#v decision=%q", resolved, decision)
	}
	err = runtime.ResolveApproval(context.Background(), adapterpkg.ApprovalDecision{WorkspaceID: "workspace-1", ThreadID: thread.ID, TurnID: turn.ID, ItemID: "item-1", ApprovalID: requested.Approval.ID, Decision: "accept"})
	if !errors.Is(err, adapterpkg.ErrConflict) {
		t.Fatalf("expired ResolveApproval error = %v", err)
	}
}

func TestAdapterAllowsUnknownVersionAndDefersCompatibilityToRuntime(t *testing.T) {
	threads := newMemoryThreadStore()
	resolver := &fakeWorkspaceResolver{resolveErr: workspace.ErrStale}
	server := &syntheticServer{version: "9.9.9"}
	formal, err := New(Options{
		Config:    config.CodexAdapterConfig{Enabled: true, Binary: "synthetic", RuntimeMode: "stdio"},
		Processes: newScriptedProcessManager(server.serve), Workspaces: resolver, Threads: threads,
	})
	if err != nil {
		t.Fatal(err)
	}
	installation, err := formal.Detect(context.Background())
	if err != nil || installation.Version != "9.9.9" {
		t.Fatalf("unknown version detection = %#v, %v", installation, err)
	}
	runtimeValue, err := formal.StartRuntime(context.Background())
	if err != nil {
		t.Fatalf("unknown version runtime start = %v", err)
	}
	_ = runtimeValue.Close(context.Background())
}

func waitForEvent(t *testing.T, events <-chan adapterpkg.AgentEvent, eventType protocol.EventType) adapterpkg.AgentEvent {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case event, ok := <-events:
			if !ok {
				t.Fatal("event channel closed")
			}
			if event.Type == eventType {
				return event
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %s", eventType)
		}
	}
}

type scriptedProcessManager struct {
	inner *fake.ProcessManager
	serve func(platform.ProcessSpec, *fake.Process)
}

func newScriptedProcessManager(serve func(platform.ProcessSpec, *fake.Process)) *scriptedProcessManager {
	return &scriptedProcessManager{inner: fake.NewProcessManager(), serve: serve}
}

func (*scriptedProcessManager) Available() bool { return true }

func (m *scriptedProcessManager) Start(ctx context.Context, spec platform.ProcessSpec) (platform.Process, error) {
	process, err := m.inner.Start(ctx, spec)
	if err != nil {
		return nil, err
	}
	go m.serve(spec, m.inner.LastProcess())
	return process, nil
}

type syntheticServer struct {
	mu                sync.Mutex
	workspace         string
	version           string
	crashOnTurnStart  bool
	approvalMethod    string
	sawWorkspaceWrite bool
	sawCwd            string
	approvalDecision  string
}

func (s *syntheticServer) serve(spec platform.ProcessSpec, process *fake.Process) {
	if len(spec.Args) == 1 && spec.Args[0] == "--version" {
		version := s.version
		if version == "" {
			version = BaselineVersion
		}
		_ = process.WriteStdout([]byte("codex-cli " + version + "\n"))
		_ = process.Complete(0)
		return
	}
	scanner := bufio.NewScanner(process.Input())
	for scanner.Scan() {
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			Result json.RawMessage `json:"result"`
		}
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			continue
		}
		id := string(request.ID)
		switch request.Method {
		case "initialize":
			version := s.version
			if version == "" {
				version = BaselineVersion
			}
			s.write(process, `{"id":%s,"result":{"userAgent":"codex_cli_rs/%s"}}`, id, version)
		case "account/read":
			s.write(process, `{"id":%s,"result":{"account":{"type":"apiKey"}}}`, id)
		case "thread/start":
			s.write(process, `{"id":%s,"result":{"thread":%s}}`, id, s.threadJSON("idle", nil))
		case "thread/list":
			s.write(process, `{"id":%s,"result":{"data":[%s],"nextCursor":null}}`, id, s.threadJSON("idle", nil))
		case "thread/read", "thread/resume":
			s.write(process, `{"id":%s,"result":{"thread":%s}}`, id, s.threadJSON("idle", []map[string]any{}))
		case "turn/start":
			if s.crashOnTurnStart {
				_ = process.Complete(13)
				return
			}
			var params map[string]any
			_ = json.Unmarshal(request.Params, &params)
			s.mu.Lock()
			s.sawCwd, _ = params["cwd"].(string)
			policy, _ := params["sandboxPolicy"].(map[string]any)
			s.sawWorkspaceWrite = policy["type"] == "workspaceWrite" && policy["networkAccess"] == false
			s.mu.Unlock()
			s.write(process, `{"id":%s,"result":{"turn":{"id":"turn-1","status":"inProgress","items":[]}}}`, id)
			s.write(process, `{"method":"turn/started","params":{"threadId":"thread-1","turn":{"id":"turn-1","status":"inProgress","items":[]}}}`)
			if s.approvalMethod == "file" {
				s.write(process, `{"id":"approval-1","method":"item/fileChange/requestApproval","params":{"threadId":"thread-1","turnId":"turn-1","itemId":"item-1","startedAtMs":1,"grantRoot":%q}}`, s.workspace)
			} else {
				s.write(process, `{"id":"approval-1","method":"item/commandExecution/requestApproval","params":{"threadId":"thread-1","turnId":"turn-1","itemId":"item-1","startedAtMs":1,"command":"synthetic-command","cwd":%q}}`, s.workspace)
			}
		case "":
			if len(request.Result) > 0 {
				var response struct {
					Result struct {
						Decision string `json:"decision"`
					} `json:"result"`
				}
				_ = json.Unmarshal(scanner.Bytes(), &response)
				s.mu.Lock()
				s.approvalDecision = response.Result.Decision
				s.mu.Unlock()
				s.write(process, `{"method":"serverRequest/resolved","params":{"threadId":"thread-1","requestId":"approval-1"}}`)
				s.write(process, `{"method":"turn/completed","params":{"threadId":"thread-1","turn":{"id":"turn-1","status":"completed","items":[]}}}`)
			}
		case "turn/interrupt", "turn/steer":
			s.write(process, `{"id":%s,"result":{}}`, id)
		}
	}
	_ = process.Complete(0)
}

func (s *syntheticServer) write(process *fake.Process, format string, values ...any) {
	_, _ = io.WriteString(io.Discard, "")
	_ = process.WriteStdout([]byte(fmt.Sprintf(format, values...) + "\n"))
}

func (s *syntheticServer) observations() (bool, string, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sawWorkspaceWrite, s.sawCwd, s.approvalDecision
}

func (s *syntheticServer) waitDecision(timeout time.Duration) string {
	deadline := time.Now().Add(timeout)
	for {
		_, _, decision := s.observations()
		if decision != "" || time.Now().After(deadline) {
			return decision
		}
		time.Sleep(time.Millisecond)
	}
}

func (s *syntheticServer) threadJSON(status string, turns any) string {
	if turns == nil {
		turns = []map[string]any{}
	}
	value := map[string]any{"id": "thread-1", "cwd": s.workspace, "createdAt": 1, "updatedAt": 2, "status": map[string]any{"type": status}, "turns": turns}
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

type fakeWorkspaceResolver struct {
	resolved   workspace.ResolvedWorkspace
	resolveErr error
}

func (f *fakeWorkspaceResolver) Resolve(ctx context.Context, id string) (workspace.ResolvedWorkspace, error) {
	if err := ctx.Err(); err != nil {
		return workspace.ResolvedWorkspace{}, err
	}
	if f.resolveErr != nil {
		return workspace.ResolvedWorkspace{}, f.resolveErr
	}
	if id != f.resolved.ID {
		return workspace.ResolvedWorkspace{}, workspace.ErrNotFound
	}
	return f.resolved, nil
}

func (f *fakeWorkspaceResolver) ResolvePath(ctx context.Context, id, logical string, intent workspace.PathIntent) (workspace.ResolvedPath, error) {
	resolved, err := f.Resolve(ctx, id)
	if err != nil {
		return workspace.ResolvedPath{}, err
	}
	if logical == "" || filepath.IsAbs(logical) {
		return workspace.ResolvedPath{}, workspace.ErrInvalid
	}
	return workspace.ResolvedPath{Workspace: resolved, Path: filepath.Join(resolved.CanonicalPath, filepath.FromSlash(logical)), Exists: true}, nil
}

type memoryThreadStore struct {
	mu      sync.RWMutex
	records map[string]store.RuntimeThreadRecord
}

func newMemoryThreadStore() *memoryThreadStore {
	return &memoryThreadStore{records: make(map[string]store.RuntimeThreadRecord)}
}

func (m *memoryThreadStore) SaveRuntimeThread(ctx context.Context, record store.RuntimeThreadRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	m.records[record.ThreadID] = record
	m.mu.Unlock()
	return nil
}

func (m *memoryThreadStore) RuntimeThread(ctx context.Context, id string) (store.RuntimeThreadRecord, error) {
	if err := ctx.Err(); err != nil {
		return store.RuntimeThreadRecord{}, err
	}
	m.mu.RLock()
	record, ok := m.records[id]
	m.mu.RUnlock()
	if !ok {
		return store.RuntimeThreadRecord{}, store.ErrNotFound
	}
	return record, nil
}

func (m *memoryThreadStore) RuntimeThreads(ctx context.Context) ([]store.RuntimeThreadRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	records := make([]store.RuntimeThreadRecord, 0, len(m.records))
	for _, record := range m.records {
		records = append(records, record)
	}
	return records, nil
}
