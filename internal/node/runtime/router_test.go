package runtime_test

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/yuanshu-ai/yuanshu/internal/adapter"
	noderuntime "github.com/yuanshu-ai/yuanshu/internal/node/runtime"
	"github.com/yuanshu-ai/yuanshu/internal/node/store"
)

func TestRouterIsolatesSameNativeSessionAcrossInstances(t *testing.T) {
	bindings := newBindingStore()
	bindings.links["workspace"] = []store.WorkspaceAgentRecord{
		{WorkspaceID: "workspace", InstanceID: "codex-office", Default: true},
		{WorkspaceID: "workspace", InstanceID: "codex-home"},
	}
	office, home := newRecordingRuntime("native-session"), newRecordingRuntime("native-session")
	router, err := noderuntime.NewRouter(noderuntime.RouterOptions{
		Store: bindings,
		Sources: []noderuntime.Source{
			{Key: noderuntime.RuntimeKey{InstanceID: "codex-office", EndpointID: "codex-office-managed"}, Runtime: office},
			{Key: noderuntime.RuntimeKey{InstanceID: "codex-home", EndpointID: "codex-home-managed"}, Runtime: home},
		},
		DefaultInstanceID: "codex-office",
		Random: bytes.NewReader([]byte{
			1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18,
			19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32, 33, 34, 35, 36,
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer router.Close(context.Background())

	officeTask, err := router.StartThread(context.Background(), adapter.StartThreadRequest{WorkspaceID: "workspace"})
	if err != nil {
		t.Fatal(err)
	}
	homeTask, err := router.StartThread(context.Background(), adapter.StartThreadRequest{WorkspaceID: "workspace", AgentInstanceID: "codex-home"})
	if err != nil {
		t.Fatal(err)
	}
	if officeTask.ID == homeTask.ID || officeTask.ID == "native-session" || homeTask.ID == "native-session" {
		t.Fatalf("opaque task IDs are not isolated: office=%q home=%q", officeTask.ID, homeTask.ID)
	}
	if officeTask.AgentInstanceID != "codex-office" || homeTask.AgentInstanceID != "codex-home" {
		t.Fatalf("instance projection mismatch: office=%#v home=%#v", officeTask, homeTask)
	}

	if _, err := router.StartTurn(context.Background(), adapter.StartTurnRequest{WorkspaceID: "workspace", ThreadID: homeTask.ID, Input: "continue"}); err != nil {
		t.Fatal(err)
	}
	if got := home.lastThreadID(); got != "native-session" {
		t.Fatalf("home runtime received task ID instead of native session: %q", got)
	}
	if got := office.lastThreadID(); got != "" {
		t.Fatalf("office runtime received home task control: %q", got)
	}
	if err := router.ResolveInteraction(context.Background(), adapter.InteractionDecision{WorkspaceID: "workspace", ThreadID: homeTask.ID, TurnID: "run", ItemID: "item", InteractionID: "interaction", Answers: []adapter.InteractionAnswer{{QuestionID: "q1", Answers: []string{"o1"}}}}); err != nil {
		t.Fatal(err)
	}
	if got := home.lastInteractionThreadID(); got != "native-session" {
		t.Fatalf("home interaction used task ID instead of native session: %q", got)
	}
	if got := office.lastInteractionThreadID(); got != "" {
		t.Fatalf("office runtime received home interaction: %q", got)
	}

	go home.emit(adapter.AgentEvent{Type: "message.completed", WorkspaceID: "workspace", ThreadID: "native-session", Payload: map[string]any{"text": "done"}})
	select {
	case event := <-router.Events():
		if event.ThreadID != homeTask.ID || event.AgentInstanceID != "codex-home" {
			t.Fatalf("translated event = %#v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for routed event")
	}
}

func TestRouterRejectsWorkspaceAgentEscalation(t *testing.T) {
	bindings := newBindingStore()
	bindings.links["workspace"] = []store.WorkspaceAgentRecord{{WorkspaceID: "workspace", InstanceID: "codex-office", Default: true}}
	router, err := noderuntime.NewRouter(noderuntime.RouterOptions{
		Store: bindings,
		Sources: []noderuntime.Source{
			{Key: noderuntime.RuntimeKey{InstanceID: "codex-office", EndpointID: "codex-office-managed"}, Runtime: newRecordingRuntime("office")},
			{Key: noderuntime.RuntimeKey{InstanceID: "codex-home", EndpointID: "codex-home-managed"}, Runtime: newRecordingRuntime("home")},
		},
		DefaultInstanceID: "codex-office",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer router.Close(context.Background())
	_, err = router.StartThread(context.Background(), adapter.StartThreadRequest{WorkspaceID: "workspace", AgentInstanceID: "codex-home"})
	if !errors.Is(err, adapter.ErrForbidden) {
		t.Fatalf("StartThread error = %v", err)
	}
}

type memoryBindingStore struct {
	mu       sync.Mutex
	bindings map[string]store.TaskBindingRecord
	links    map[string][]store.WorkspaceAgentRecord
}

func newBindingStore() *memoryBindingStore {
	return &memoryBindingStore{bindings: make(map[string]store.TaskBindingRecord), links: make(map[string][]store.WorkspaceAgentRecord)}
}

func (s *memoryBindingStore) SaveTaskBinding(_ context.Context, value store.TaskBindingRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bindings[value.TaskID] = value
	return nil
}

func (s *memoryBindingStore) TaskBinding(_ context.Context, taskID string) (store.TaskBindingRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.bindings[taskID]
	if !ok {
		return store.TaskBindingRecord{}, store.ErrNotFound
	}
	return value, nil
}

func (s *memoryBindingStore) TaskBindingByNativeSession(_ context.Context, instanceID, nativeSessionID string) (store.TaskBindingRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, value := range s.bindings {
		if value.InstanceID == instanceID && value.NativeSessionID == nativeSessionID {
			return value, nil
		}
	}
	return store.TaskBindingRecord{}, store.ErrNotFound
}

func (s *memoryBindingStore) WorkspaceAgents(_ context.Context, workspaceID string) ([]store.WorkspaceAgentRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]store.WorkspaceAgentRecord(nil), s.links[workspaceID]...), nil
}

type recordingRuntime struct {
	mu                  sync.Mutex
	nativeSessionID     string
	threadID            string
	interactionThreadID string
	events              chan adapter.AgentEvent
	closeOnce           sync.Once
}

func newRecordingRuntime(nativeSessionID string) *recordingRuntime {
	return &recordingRuntime{nativeSessionID: nativeSessionID, events: make(chan adapter.AgentEvent)}
}

func (r *recordingRuntime) ListThreads(context.Context, adapter.ListThreadsRequest) (adapter.ThreadPage, error) {
	return adapter.ThreadPage{Data: []adapter.Thread{{ID: r.nativeSessionID, WorkspaceID: "workspace"}}}, nil
}
func (r *recordingRuntime) ReadThread(_ context.Context, request adapter.ReadThreadRequest) (adapter.ThreadSnapshot, error) {
	return adapter.ThreadSnapshot{Thread: adapter.Thread{ID: request.ThreadID, WorkspaceID: request.WorkspaceID}}, nil
}
func (r *recordingRuntime) StartThread(_ context.Context, request adapter.StartThreadRequest) (adapter.Thread, error) {
	return adapter.Thread{ID: r.nativeSessionID, WorkspaceID: request.WorkspaceID}, nil
}
func (r *recordingRuntime) ResumeThread(_ context.Context, request adapter.ResumeThreadRequest) (adapter.Thread, error) {
	return adapter.Thread{ID: request.ThreadID, WorkspaceID: request.WorkspaceID}, nil
}
func (r *recordingRuntime) StartTurn(_ context.Context, request adapter.StartTurnRequest) (adapter.Turn, error) {
	r.mu.Lock()
	r.threadID = request.ThreadID
	r.mu.Unlock()
	return adapter.Turn{ID: "run", ThreadID: request.ThreadID, Status: "running"}, nil
}
func (r *recordingRuntime) SteerTurn(context.Context, adapter.SteerTurnRequest) error { return nil }
func (r *recordingRuntime) InterruptTurn(context.Context, adapter.InterruptTurnRequest) error {
	return nil
}
func (r *recordingRuntime) ResolveApproval(context.Context, adapter.ApprovalDecision) error {
	return nil
}
func (r *recordingRuntime) ResolveInteraction(_ context.Context, decision adapter.InteractionDecision) error {
	r.mu.Lock()
	r.interactionThreadID = decision.ThreadID
	r.mu.Unlock()
	return nil
}
func (r *recordingRuntime) Events() <-chan adapter.AgentEvent { return r.events }
func (r *recordingRuntime) Health() adapter.HealthStatus {
	return adapter.HealthStatus{State: "ready"}
}
func (r *recordingRuntime) Close(context.Context) error {
	r.closeOnce.Do(func() { close(r.events) })
	return nil
}
func (r *recordingRuntime) emit(event adapter.AgentEvent) { r.events <- event }
func (r *recordingRuntime) lastThreadID() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.threadID
}
func (r *recordingRuntime) lastInteractionThreadID() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.interactionThreadID
}
