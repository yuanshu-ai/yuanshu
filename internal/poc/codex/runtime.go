// Package codex adapts the existing AC-002 Probe to the temporary M0 Node Runtime.
// It is not the formal CodexAdapter planned for AC-203.
package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/yuanshu-ai/yuanshu/internal/adapter/codex/probe"
	pocnode "github.com/yuanshu-ai/yuanshu/internal/poc/node"
	"github.com/yuanshu-ai/yuanshu/internal/poc/protocol"
)

type Runtime struct {
	client       *probe.Client
	workspace    string
	archive      bool
	mu           sync.Mutex
	threadID     string
	activeTurnID string
	approvals    map[string]probe.RequestID
	nextApproval uint64
	closed       bool
}

func Start(ctx context.Context, workspace string, archive bool) (*Runtime, error) {
	client, err := probe.Start(ctx, probe.Options{Dir: workspace, Env: probe.Environment()})
	if err != nil {
		return nil, err
	}
	title := "Yuanshu M0 PoC"
	if _, err = client.Initialize(ctx, probe.ClientInfo{Name: "yuanshu_m0_poc", Title: &title, Version: "0.0.0"}); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("initialize PoC Runtime: %w", err)
	}
	return &Runtime{client: client, workspace: workspace, archive: archive, approvals: make(map[string]probe.RequestID)}, nil
}

func (r *Runtime) Start(ctx context.Context, prompt string) (<-chan pocnode.RuntimeEvent, error) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil, errors.New("Codex PoC Runtime closed")
	}
	threadID := r.threadID
	r.mu.Unlock()
	if threadID == "" {
		var result struct {
			Thread struct {
				ID string `json:"id"`
			} `json:"thread"`
		}
		if err := r.client.Call(ctx, "thread/start", map[string]any{"cwd": r.workspace, "approvalPolicy": "on-request", "sandbox": "workspace-write", "serviceName": "yuanshu_m0_poc"}, &result); err != nil {
			return nil, fmt.Errorf("thread/start: %w", err)
		}
		if result.Thread.ID == "" {
			return nil, errors.New("thread/start returned no id")
		}
		r.mu.Lock()
		r.threadID = result.Thread.ID
		r.mu.Unlock()
		threadID = result.Thread.ID
	}
	var result struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	if err := r.client.Call(ctx, "turn/start", map[string]any{"threadId": threadID, "approvalPolicy": "on-request", "sandboxPolicy": map[string]any{"type": "readOnly"}, "input": []map[string]any{{"type": "text", "text": prompt}}}, &result); err != nil {
		return nil, fmt.Errorf("turn/start: %w", err)
	}
	if result.Turn.ID == "" {
		return nil, errors.New("turn/start returned no id")
	}
	r.mu.Lock()
	r.activeTurnID = result.Turn.ID
	r.mu.Unlock()
	out := make(chan pocnode.RuntimeEvent, 64)
	go r.readTurn(ctx, result.Turn.ID, out)
	return out, nil
}

func (r *Runtime) readTurn(ctx context.Context, turnID string, out chan<- pocnode.RuntimeEvent) {
	defer close(out)
	for {
		select {
		case msg, ok := <-r.client.Messages():
			if !ok {
				out <- pocnode.RuntimeEvent{Type: protocol.Ambiguous, Payload: map[string]string{"status": "unknown"}, Terminal: true, Ambiguous: true}
				return
			}
			if msg.IsRequest() {
				r.approval(msg, out)
				continue
			}
			event, emit, terminal := r.mapMessage(msg, turnID)
			if emit {
				out <- event
			}
			if terminal {
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

func (r *Runtime) approval(msg probe.Message, out chan<- pocnode.RuntimeEvent) {
	var scope struct {
		ThreadID string `json:"threadId"`
		TurnID   string `json:"turnId"`
	}
	if json.Unmarshal(msg.Params, &scope) != nil {
		_ = r.client.Respond(*msg.ID, nil, &probe.RPCError{Code: -32602, Message: "invalid approval scope"})
		return
	}
	r.mu.Lock()
	currentThread, currentTurn := r.threadID, r.activeTurnID
	r.mu.Unlock()
	if scope.ThreadID != currentThread || scope.TurnID != currentTurn {
		_ = r.client.Respond(*msg.ID, nil, &probe.RPCError{Code: -32602, Message: "approval is outside the active Yuanshu Turn"})
		return
	}
	kind := ""
	switch msg.Method {
	case "item/commandExecution/requestApproval":
		kind = "command"
	case "item/fileChange/requestApproval":
		kind = "file-change"
	default:
		_ = r.client.Respond(*msg.ID, nil, &probe.RPCError{Code: -32601, Message: "unsupported by Yuanshu M0 PoC"})
		return
	}
	r.mu.Lock()
	r.nextApproval++
	handle := fmt.Sprintf("a-%d", r.nextApproval)
	r.approvals[handle] = *msg.ID
	r.mu.Unlock()
	out <- pocnode.RuntimeEvent{Approval: &pocnode.RuntimeApproval{Handle: handle, Kind: kind, Summary: kind + " approval required"}}
}

func (r *Runtime) Resolve(_ context.Context, handle, decision string) error {
	r.mu.Lock()
	id, ok := r.approvals[handle]
	if ok {
		delete(r.approvals, handle)
	}
	r.mu.Unlock()
	if !ok {
		return errors.New("Codex approval is no longer pending")
	}
	return r.client.Respond(id, map[string]string{"decision": decision}, nil)
}

func (r *Runtime) mapMessage(msg probe.Message, turnID string) (pocnode.RuntimeEvent, bool, bool) {
	switch msg.Method {
	case "item/agentMessage/delta":
		var p struct{ TurnID, Delta string }
		if json.Unmarshal(msg.Params, &p) != nil || p.TurnID != turnID {
			return pocnode.RuntimeEvent{}, false, false
		}
		return pocnode.RuntimeEvent{Type: protocol.AgentDelta, Payload: map[string]string{"delta": p.Delta}}, true, false
	case "turn/diff/updated":
		var p struct{ TurnID, Diff string }
		if json.Unmarshal(msg.Params, &p) != nil || p.TurnID != turnID {
			return pocnode.RuntimeEvent{}, false, false
		}
		return pocnode.RuntimeEvent{Type: protocol.DiffUpdated, Payload: map[string]string{"diff": r.safeText(p.Diff)}}, true, false
	case "item/started", "item/completed":
		var p struct {
			TurnID string `json:"turnId"`
			Item   struct {
				Type, Status string
				Changes      []struct{ Path, Kind, Diff string }
			} `json:"item"`
		}
		if json.Unmarshal(msg.Params, &p) != nil || p.TurnID != turnID {
			return pocnode.RuntimeEvent{}, false, false
		}
		switch p.Item.Type {
		case "commandExecution":
			return pocnode.RuntimeEvent{Type: protocol.CommandEvent, Payload: map[string]string{"status": p.Item.Status}}, true, false
		case "fileChange":
			changes := make([]map[string]string, 0, len(p.Item.Changes))
			for _, c := range p.Item.Changes {
				changes = append(changes, map[string]string{"path": filepath.Base(c.Path), "kind": c.Kind, "diff": r.safeText(c.Diff)})
			}
			return pocnode.RuntimeEvent{Type: protocol.FileChange, Payload: map[string]any{"status": p.Item.Status, "changes": changes}}, true, false
		}
	case "turn/completed":
		var p struct {
			Turn struct{ ID, Status string } `json:"turn"`
		}
		if json.Unmarshal(msg.Params, &p) != nil || p.Turn.ID != turnID {
			return pocnode.RuntimeEvent{}, false, false
		}
		r.mu.Lock()
		r.activeTurnID = ""
		r.mu.Unlock()
		kind := protocol.TurnCompleted
		if p.Turn.Status != "completed" {
			kind = protocol.TurnFailed
		}
		return pocnode.RuntimeEvent{Type: kind, Payload: map[string]string{"status": p.Turn.Status}, Terminal: true}, true, true
	}
	return pocnode.RuntimeEvent{}, false, false
}

func (r *Runtime) safeText(v string) string { return strings.ReplaceAll(v, r.workspace, "<WORKSPACE>") }

func (r *Runtime) Close() error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	threadID := r.threadID
	archive := r.archive
	r.mu.Unlock()
	var archiveErr error
	if archive && threadID != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		archiveErr = r.client.Call(ctx, "thread/archive", map[string]any{"threadId": threadID}, nil)
		cancel()
	}
	return errors.Join(archiveErr, r.client.Close())
}
