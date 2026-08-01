package codex

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"path/filepath"
	"strings"
	"time"

	"github.com/yuanshu-ai/yuanshu/internal/adapter"
	"github.com/yuanshu-ai/yuanshu/internal/adapter/codex/appserver"
	"github.com/yuanshu-ai/yuanshu/internal/config"
	"github.com/yuanshu-ai/yuanshu/internal/node/store"
	"github.com/yuanshu-ai/yuanshu/internal/node/workspace"
	protocol "github.com/yuanshu-ai/yuanshu/internal/protocol/v1"
)

func (r *Runtime) consume(client *appserver.Client, generation uint64) {
	for message := range client.Messages() {
		if message.IsRequest() {
			r.handleServerRequest(client, message)
		} else {
			r.handleNotification(message)
		}
	}
	r.handleClientLoss(client, generation)
}

func (r *Runtime) handleClientLoss(client *appserver.Client, generation uint64) {
	r.mu.Lock()
	if r.closed || r.client != client || r.generation != generation {
		r.mu.Unlock()
		return
	}
	r.client = nil
	r.health.State = "unavailable"
	r.health.FailureCode = "app_server_exited"
	activeThread, activeTurn := r.activeThread, r.activeTurn
	for id, pending := range r.pending {
		pending.timer.Stop()
		delete(r.pending, id)
	}
	r.mu.Unlock()
	if activeThread != "" {
		if record, err := r.options.Threads.RuntimeThread(context.Background(), activeThread); err == nil {
			record.State = store.RuntimeThreadNeedsReconcile
			if record.ActiveTurnID == "" {
				record.ActiveTurnID = activeTurn
			}
			_ = r.options.Threads.SaveRuntimeThread(context.Background(), record)
		}
		r.emit(adapter.AgentEvent{
			Type: protocol.EventError, ThreadID: activeThread, TurnID: activeTurn, Ambiguous: true,
			Payload: map[string]any{"code": "ambiguous", "message": "Runtime exited before the active Turn was confirmed."},
		})
	}
	r.emit(adapter.AgentEvent{Type: protocol.EventRuntimeStatus, Payload: map[string]any{"state": "unavailable"}})
}

func (r *Runtime) handleServerRequest(client *appserver.Client, message appserver.Message) {
	switch message.Method {
	case "item/commandExecution/requestApproval":
		r.handleCommandApproval(client, message)
	case "item/fileChange/requestApproval":
		r.handleFileApproval(client, message)
	default:
		_ = client.Respond(*message.ID, nil, &appserver.RPCError{Code: -32601, Message: "unsupported by Yuanshu"})
	}
}

type approvalScope struct {
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
	ItemID   string `json:"itemId"`
}

func (r *Runtime) handleCommandApproval(client *appserver.Client, message appserver.Message) {
	var params struct {
		approvalScope
		Command        string          `json:"command"`
		CommandActions json.RawMessage `json:"commandActions"`
		Cwd            string          `json:"cwd"`
		Network        json.RawMessage `json:"networkApprovalContext"`
	}
	if json.Unmarshal(message.Params, &params) != nil || !validID(params.ThreadID) || !validID(params.TurnID) || !validID(params.ItemID) {
		_ = client.Respond(*message.ID, map[string]string{"decision": "decline"}, nil)
		return
	}
	resolved, ok := r.approvalWorkspace(params.approvalScope)
	if !ok {
		_ = client.Respond(*message.ID, map[string]string{"decision": "decline"}, nil)
		return
	}
	cwd, ok := r.logicalApprovalPath(resolved, params.Cwd)
	if !ok {
		_ = client.Respond(*message.ID, map[string]string{"decision": "decline"}, nil)
		return
	}
	operation := map[string]any{"command": params.Command, "cwd": cwd}
	if len(params.CommandActions) > 0 && string(params.CommandActions) != "null" {
		var actions any
		if json.Unmarshal(params.CommandActions, &actions) == nil {
			operation["commandActions"] = actions
		}
	}
	if len(params.Network) > 0 && string(params.Network) != "null" {
		if !resolved.AllowNetwork {
			_ = client.Respond(*message.ID, map[string]string{"decision": "decline"}, nil)
			return
		}
		var network any
		if json.Unmarshal(params.Network, &network) == nil {
			operation["network"] = network
		}
	}
	r.registerApproval(client, message, params.approvalScope, resolved.ID, "command", "Command execution approval required", operation)
}

func (r *Runtime) handleFileApproval(client *appserver.Client, message appserver.Message) {
	var params struct {
		approvalScope
		GrantRoot string `json:"grantRoot"`
		Reason    string `json:"reason"`
	}
	if json.Unmarshal(message.Params, &params) != nil || !validID(params.ThreadID) || !validID(params.TurnID) || !validID(params.ItemID) {
		_ = client.Respond(*message.ID, map[string]string{"decision": "decline"}, nil)
		return
	}
	resolved, ok := r.approvalWorkspace(params.approvalScope)
	if !ok {
		_ = client.Respond(*message.ID, map[string]string{"decision": "decline"}, nil)
		return
	}
	if resolved.PermissionProfile != config.PermissionWorkspaceWrite {
		_ = client.Respond(*message.ID, map[string]string{"decision": "decline"}, nil)
		return
	}
	operation := map[string]any{}
	if params.GrantRoot != "" {
		grantRoot, valid := r.logicalApprovalPath(resolved, params.GrantRoot)
		if !valid {
			_ = client.Respond(*message.ID, map[string]string{"decision": "decline"}, nil)
			return
		}
		operation["grantRoot"] = grantRoot
	}
	if params.Reason != "" {
		operation["reason"] = params.Reason
	}
	r.registerApproval(client, message, params.approvalScope, resolved.ID, "file-change", "File change approval required", operation)
}

func (r *Runtime) approvalWorkspace(scope approvalScope) (workspace.ResolvedWorkspace, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	record, err := r.options.Threads.RuntimeThread(ctx, scope.ThreadID)
	if err != nil || record.State != store.RuntimeThreadActive || record.ActiveTurnID != scope.TurnID {
		return workspace.ResolvedWorkspace{}, false
	}
	resolved, err := r.resolveWorkspace(ctx, record.WorkspaceID)
	return resolved, err == nil
}

func (r *Runtime) logicalApprovalPath(resolved workspace.ResolvedWorkspace, absolute string) (string, bool) {
	if absolute == "" {
		return ".", true
	}
	relative, err := filepath.Rel(resolved.CanonicalPath, filepath.Clean(absolute))
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return "", false
	}
	if relative == "." {
		return ".", true
	}
	logical := filepath.ToSlash(relative)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := r.options.Workspaces.ResolvePath(ctx, resolved.ID, logical, workspace.PathRead); err != nil {
		return "", false
	}
	return logical, true
}

func (r *Runtime) registerApproval(client *appserver.Client, message appserver.Message, scope approvalScope, workspaceID, kind, summary string, operation any) {
	id, err := randomApprovalID()
	if err != nil {
		_ = client.Respond(*message.ID, map[string]string{"decision": "decline"}, nil)
		return
	}
	pending := &pendingApproval{id: id, requestID: *message.ID, workspaceID: workspaceID, threadID: scope.ThreadID, turnID: scope.TurnID, itemID: scope.ItemID, kind: kind}
	pending.timer = time.AfterFunc(r.options.ApprovalTimeout, func() { r.expireApproval(id) })
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		pending.timer.Stop()
		_ = client.Respond(*message.ID, map[string]string{"decision": "decline"}, nil)
		return
	}
	r.pending[id] = pending
	r.mu.Unlock()
	r.emit(adapter.AgentEvent{
		Type: protocol.EventApprovalRequested, WorkspaceID: workspaceID, ThreadID: scope.ThreadID, TurnID: scope.TurnID, ItemID: scope.ItemID,
		Approval: &adapter.Approval{ID: id, Kind: kind, Summary: summary, Operation: operation, ExpiresAt: time.Now().Add(r.options.ApprovalTimeout).UTC()},
		Payload:  map[string]any{"approvalId": id, "kind": kind, "summary": summary},
	})
}

func (r *Runtime) expireApproval(id string) {
	r.mu.Lock()
	pending, ok := r.pending[id]
	if ok {
		delete(r.pending, id)
	}
	client := r.client
	r.mu.Unlock()
	if !ok || client == nil {
		return
	}
	_ = client.Respond(pending.requestID, map[string]string{"decision": "decline"}, nil)
	r.emit(adapter.AgentEvent{Type: protocol.EventApprovalResolved, WorkspaceID: pending.workspaceID, ThreadID: pending.threadID, TurnID: pending.turnID, ItemID: pending.itemID, Payload: map[string]any{"approvalId": id, "decision": "decline", "reason": "expired"}})
}

func randomApprovalID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return "apr_" + base64.RawURLEncoding.EncodeToString(value), nil
}

func (r *Runtime) handleNotification(message appserver.Message) {
	switch message.Method {
	case "serverRequest/resolved":
		r.handleRequestResolved(message)
	case "turn/started":
		r.handleTurnStarted(message)
	case "turn/completed":
		r.handleTurnCompleted(message)
	case "item/agentMessage/delta":
		r.handleDelta(message, protocol.EventAgentMessageDelta, "delta")
	case "item/commandExecution/outputDelta":
		r.handleDelta(message, protocol.EventCommandOutputDelta, "delta")
	case "turn/diff/updated":
		r.handleDelta(message, protocol.EventDiffUpdated, "diff")
	case "item/started", "item/completed":
		r.handleItem(message)
	case "thread/started":
		r.handleThreadStarted(message)
	}
}

func (r *Runtime) eventRecord(threadID string) (store.RuntimeThreadRecord, bool) {
	record, err := r.options.Threads.RuntimeThread(context.Background(), threadID)
	return record, err == nil && record.Adapter == AdapterID
}

func (r *Runtime) handleThreadStarted(message appserver.Message) {
	var params struct {
		Thread codexThread `json:"thread"`
	}
	if json.Unmarshal(message.Params, &params) != nil {
		return
	}
	record, ok := r.eventRecord(params.Thread.ID)
	if !ok {
		return
	}
	r.emit(adapter.AgentEvent{Type: protocol.EventThreadStarted, WorkspaceID: record.WorkspaceID, ThreadID: record.ThreadID, Payload: map[string]any{"status": params.Thread.Status.Type}})
}

func (r *Runtime) handleTurnStarted(message appserver.Message) {
	var params struct {
		ThreadID string    `json:"threadId"`
		Turn     codexTurn `json:"turn"`
	}
	if json.Unmarshal(message.Params, &params) != nil {
		return
	}
	record, ok := r.eventRecord(params.ThreadID)
	if !ok {
		return
	}
	r.emit(adapter.AgentEvent{Type: protocol.EventTurnStarted, WorkspaceID: record.WorkspaceID, ThreadID: record.ThreadID, TurnID: params.Turn.ID, Payload: map[string]any{"status": params.Turn.Status}})
}

func (r *Runtime) handleTurnCompleted(message appserver.Message) {
	var params struct {
		ThreadID string    `json:"threadId"`
		Turn     codexTurn `json:"turn"`
	}
	if json.Unmarshal(message.Params, &params) != nil {
		return
	}
	record, ok := r.eventRecord(params.ThreadID)
	if !ok || record.ActiveTurnID != "" && record.ActiveTurnID != params.Turn.ID {
		return
	}
	record.State, record.ActiveTurnID = store.RuntimeThreadIdle, ""
	_ = r.options.Threads.SaveRuntimeThread(context.Background(), record)
	r.clearActive(params.ThreadID, params.Turn.ID)
	r.clearApprovals(params.ThreadID, params.Turn.ID)
	eventType := protocol.EventTurnCompleted
	switch params.Turn.Status {
	case "interrupted":
		eventType = protocol.EventTurnInterrupted
	case "failed":
		eventType = protocol.EventTurnFailed
	}
	r.emit(adapter.AgentEvent{Type: eventType, WorkspaceID: record.WorkspaceID, ThreadID: record.ThreadID, TurnID: params.Turn.ID, Payload: map[string]any{"status": params.Turn.Status}})
}

func (r *Runtime) handleDelta(message appserver.Message, eventType protocol.EventType, field string) {
	var params struct {
		ThreadID string `json:"threadId"`
		TurnID   string `json:"turnId"`
		ItemID   string `json:"itemId"`
		Delta    string `json:"delta"`
		Diff     string `json:"diff"`
	}
	if json.Unmarshal(message.Params, &params) != nil {
		return
	}
	record, ok := r.eventRecord(params.ThreadID)
	if !ok {
		return
	}
	value := params.Delta
	if field == "diff" {
		value = params.Diff
		if resolved, err := r.options.Workspaces.Resolve(context.Background(), record.WorkspaceID); err == nil {
			value = redactWorkspace(value, resolved.CanonicalPath)
		}
	}
	r.emit(adapter.AgentEvent{Type: eventType, WorkspaceID: record.WorkspaceID, ThreadID: params.ThreadID, TurnID: params.TurnID, ItemID: params.ItemID, Payload: map[string]any{field: value}})
}

func (r *Runtime) handleItem(message appserver.Message) {
	var params struct {
		ThreadID string `json:"threadId"`
		TurnID   string `json:"turnId"`
		Item     struct {
			ID      string `json:"id"`
			Type    string `json:"type"`
			Status  string `json:"status"`
			Text    string `json:"text"`
			Command string `json:"command"`
			Changes []struct {
				Path string `json:"path"`
				Kind string `json:"kind"`
				Diff string `json:"diff"`
			} `json:"changes"`
		} `json:"item"`
	}
	if json.Unmarshal(message.Params, &params) != nil {
		return
	}
	record, ok := r.eventRecord(params.ThreadID)
	if !ok {
		return
	}
	completed := message.Method == "item/completed"
	eventType := protocol.EventToolStarted
	payload := map[string]any{"status": params.Item.Status, "kind": params.Item.Type}
	switch params.Item.Type {
	case "agentMessage":
		if !completed {
			return
		}
		eventType = protocol.EventAgentMessageCompleted
		payload = map[string]any{"text": params.Item.Text}
	case "commandExecution":
		if completed {
			eventType = protocol.EventCommandCompleted
		} else {
			eventType = protocol.EventCommandStarted
		}
		payload["command"] = params.Item.Command
	case "fileChange":
		if !completed {
			return
		}
		eventType = protocol.EventFileChanged
		resolved, err := r.options.Workspaces.Resolve(context.Background(), record.WorkspaceID)
		if err != nil {
			return
		}
		changes := make([]map[string]any, 0, len(params.Item.Changes))
		for _, change := range params.Item.Changes {
			logical, valid := logicalExistingOrFuture(resolved.CanonicalPath, change.Path)
			if !valid {
				continue
			}
			changes = append(changes, map[string]any{"path": logical, "kind": change.Kind, "diff": redactWorkspace(change.Diff, resolved.CanonicalPath)})
		}
		payload = map[string]any{"status": params.Item.Status, "changes": changes}
	default:
		if completed {
			eventType = protocol.EventToolCompleted
		}
	}
	r.emit(adapter.AgentEvent{Type: eventType, WorkspaceID: record.WorkspaceID, ThreadID: params.ThreadID, TurnID: params.TurnID, ItemID: params.Item.ID, Payload: payload})
}

func (r *Runtime) handleRequestResolved(message appserver.Message) {
	var params struct {
		ThreadID  string          `json:"threadId"`
		RequestID json.RawMessage `json:"requestId"`
	}
	if json.Unmarshal(message.Params, &params) != nil {
		return
	}
	r.mu.Lock()
	var resolved *pendingApproval
	for id, pending := range r.pending {
		if pending.threadID == params.ThreadID && pending.requestID.Matches(params.RequestID) {
			resolved = pending
			pending.timer.Stop()
			delete(r.pending, id)
			break
		}
	}
	r.mu.Unlock()
	if resolved != nil {
		r.emit(adapter.AgentEvent{Type: protocol.EventApprovalResolved, WorkspaceID: resolved.workspaceID, ThreadID: resolved.threadID, TurnID: resolved.turnID, ItemID: resolved.itemID, Payload: map[string]any{"approvalId": resolved.id, "reason": "runtime_resolved"}})
	}
}

func (r *Runtime) clearApprovals(threadID, turnID string) {
	r.mu.Lock()
	for id, pending := range r.pending {
		if pending.threadID == threadID && pending.turnID == turnID {
			pending.timer.Stop()
			delete(r.pending, id)
		}
	}
	r.mu.Unlock()
}

func redactWorkspace(value, root string) string {
	value = strings.ReplaceAll(value, root, "<WORKSPACE>")
	return strings.ReplaceAll(value, filepath.ToSlash(root), "<WORKSPACE>")
}

func logicalExistingOrFuture(root, path string) (string, bool) {
	relative, err := filepath.Rel(root, filepath.Clean(path))
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return "", false
	}
	return filepath.ToSlash(relative), true
}
