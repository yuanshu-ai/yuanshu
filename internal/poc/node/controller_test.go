package node

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/yuanshu-ai/yuanshu/internal/poc/protocol"
	"github.com/yuanshu-ai/yuanshu/internal/poc/transport"
)

type fakeRuntime struct {
	events    chan RuntimeEvent
	mu        sync.Mutex
	starts    int
	decisions []string
}

func (f *fakeRuntime) Start(context.Context, string) (<-chan RuntimeEvent, error) {
	f.mu.Lock()
	f.starts++
	f.mu.Unlock()
	return f.events, nil
}
func (f *fakeRuntime) Resolve(_ context.Context, _ string, d string) error {
	f.mu.Lock()
	f.decisions = append(f.decisions, d)
	f.mu.Unlock()
	return nil
}
func (f *fakeRuntime) Close() error { return nil }

func sendFrame(t *testing.T, ep transport.Endpoint, kind string, p any) {
	t.Helper()
	f, err := protocol.New(kind, "request", "poc-node", p)
	if err != nil {
		t.Fatal(err)
	}
	if err := ep.Send(context.Background(), f); err != nil {
		t.Fatal(err)
	}
}
func receiveType(t *testing.T, ep transport.Endpoint, kind string) protocol.Frame {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for {
		f, err := ep.Receive(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if f.Type == kind {
			return f
		}
	}
}

func TestStandaloneTaskApprovalReplayAndCompletion(t *testing.T) {
	runtime := &fakeRuntime{events: make(chan RuntimeEvent, 16)}
	controller := New("poc-node", runtime)
	server, node := transport.StandalonePair(32)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = controller.Run(ctx, node) }()
	receiveType(t, server, protocol.NodeStatus)
	sendFrame(t, server, protocol.TaskStart, protocol.TaskStartPayload{WorkspaceID: protocol.WorkspaceID, Prompt: "synthetic"})
	receiveType(t, server, protocol.TurnStarted)
	runtime.events <- RuntimeEvent{Type: protocol.AgentDelta, Payload: map[string]string{"delta": "safe"}}
	receiveType(t, server, protocol.AgentDelta)
	runtime.events <- RuntimeEvent{Approval: &RuntimeApproval{Handle: "raw-private-id", Kind: "file-change", Summary: "synthetic approval"}}
	approval := receiveType(t, server, protocol.ApprovalRequested)
	var body protocol.ApprovalResolvePayload
	if err := json.Unmarshal(approval.Payload, &body); err != nil {
		t.Fatal(err)
	}
	if body.ApprovalID == "" || body.ApprovalID == "raw-private-id" {
		t.Fatal("approval id was not replaced")
	}
	sendFrame(t, server, protocol.EventsResume, protocol.ResumePayload{LastSequence: approval.Sequence - 1})
	replayed := receiveType(t, server, protocol.ApprovalRequested)
	if replayed.Sequence != approval.Sequence {
		t.Fatal("approval was not replayed")
	}
	sendFrame(t, server, protocol.ApprovalResolve, protocol.ApprovalResolvePayload{ApprovalID: body.ApprovalID, Decision: "accept"})
	receiveType(t, server, protocol.ApprovalResolved)
	runtime.events <- RuntimeEvent{Type: protocol.TurnCompleted, Payload: map[string]string{"status": "completed"}, Terminal: true}
	receiveType(t, server, protocol.TurnCompleted)
	sendFrame(t, server, protocol.ApprovalResolve, protocol.ApprovalResolvePayload{ApprovalID: body.ApprovalID, Decision: "accept"})
	receiveType(t, server, protocol.ErrorEvent)
}

func TestSecondTurnAndApprovalTimeoutAreRejectedSafely(t *testing.T) {
	runtime := &fakeRuntime{events: make(chan RuntimeEvent, 16)}
	controller := New("poc-node", runtime)
	controller.SetApprovalTimeoutForTest(20 * time.Millisecond)
	server, node := transport.StandalonePair(32)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = controller.Run(ctx, node) }()
	receiveType(t, server, protocol.NodeStatus)
	p := protocol.TaskStartPayload{WorkspaceID: protocol.WorkspaceID, Prompt: "synthetic"}
	sendFrame(t, server, protocol.TaskStart, p)
	receiveType(t, server, protocol.TurnStarted)
	sendFrame(t, server, protocol.TaskStart, p)
	receiveType(t, server, protocol.ErrorEvent)
	runtime.events <- RuntimeEvent{Approval: &RuntimeApproval{Handle: "private", Kind: "command", Summary: "synthetic"}}
	receiveType(t, server, protocol.ApprovalRequested)
	resolved := receiveType(t, server, protocol.ApprovalResolved)
	var payload map[string]string
	_ = json.Unmarshal(resolved.Payload, &payload)
	if payload["decision"] != "decline" {
		t.Fatal("expired approval was not declined")
	}
}

func TestOldCursorGetsGapAndSnapshot(t *testing.T) {
	runtime := &fakeRuntime{events: make(chan RuntimeEvent, MaxEvents+10)}
	controller := New("poc-node", runtime)
	server, node := transport.StandalonePair(MaxEvents + 32)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = controller.Run(ctx, node) }()
	receiveType(t, server, protocol.NodeStatus)
	sendFrame(t, server, protocol.TaskStart, protocol.TaskStartPayload{WorkspaceID: protocol.WorkspaceID, Prompt: "synthetic"})
	receiveType(t, server, protocol.TurnStarted)
	for i := 0; i < MaxEvents+5; i++ {
		runtime.events <- RuntimeEvent{Type: protocol.AgentDelta, Payload: map[string]int{"part": i}}
		receiveType(t, server, protocol.AgentDelta)
	}
	sendFrame(t, server, protocol.EventsResume, protocol.ResumePayload{LastSequence: 0})
	receiveType(t, server, protocol.HistoryGap)
	snapshot := receiveType(t, server, protocol.Snapshot)
	if string(snapshot.Payload) == "" {
		t.Fatal("missing snapshot")
	}
}

func TestArchiveModeStopsAfterTerminal(t *testing.T) {
	runtime := &fakeRuntime{events: make(chan RuntimeEvent, 2)}
	controller := New("poc-node", runtime)
	controller.StopAfterTerminal()
	server, node := transport.StandalonePair(8)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- controller.Run(ctx, node) }()
	receiveType(t, server, protocol.NodeStatus)
	sendFrame(t, server, protocol.TaskStart, protocol.TaskStartPayload{WorkspaceID: protocol.WorkspaceID, Prompt: "synthetic"})
	receiveType(t, server, protocol.TurnStarted)
	runtime.events <- RuntimeEvent{Type: protocol.TurnCompleted, Payload: map[string]string{"status": "completed"}, Terminal: true}
	receiveType(t, server, protocol.TurnCompleted)
	if err := <-done; err != nil {
		t.Fatalf("archive-mode Controller returned %v", err)
	}
}
