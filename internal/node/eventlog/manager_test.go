package eventlog

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yuanshu-ai/yuanshu/internal/adapter"
	"github.com/yuanshu-ai/yuanshu/internal/node/store"
	protocol "github.com/yuanshu-ai/yuanshu/internal/protocol/v1"
)

var eventNow = time.Date(2026, 8, 2, 10, 0, 0, 0, time.UTC)

func TestPublishMapsRedactsAndPersistsProtocolEvents(t *testing.T) {
	manager, local := newTestManager(t, 16<<20)
	canary := "sk-eventlog-secret-canary"
	events := []adapter.AgentEvent{
		{Type: protocol.EventRuntimeStatus, Payload: map[string]any{"state": "ready"}},
		{Type: protocol.EventAgentMessageDelta, WorkspaceID: "workspace", ThreadID: "thread", TurnID: "turn", ItemID: "item", Payload: map[string]any{"delta": "hello " + canary}},
		{Type: protocol.EventCommandOutputDelta, WorkspaceID: "workspace", ThreadID: "thread", TurnID: "turn", ItemID: "command", Payload: map[string]any{"delta": "output"}},
		{Type: protocol.EventDiffUpdated, WorkspaceID: "workspace", ThreadID: "thread", TurnID: "turn", Payload: map[string]any{"diff": "+synthetic"}},
		{Type: protocol.EventApprovalRequested, WorkspaceID: "workspace", ThreadID: "thread", TurnID: "turn", ItemID: "item", Approval: &adapter.Approval{ID: "approval", Kind: "command", Summary: "Approve", Operation: map[string]any{"command": "echo synthetic"}, ExpiresAt: eventNow.Add(time.Minute)}},
	}
	var sequence int64
	for _, event := range events {
		records, err := manager.Publish(context.Background(), event)
		if err != nil || len(records) != 1 {
			t.Fatalf("Publish(%s) = %#v, %v", event.Type, records, err)
		}
		sequence++
		if records[0].Sequence != sequence {
			t.Fatalf("sequence = %d, want %d", records[0].Sequence, sequence)
		}
		message, err := protocol.ParseEvent(records[0].Frame)
		if err != nil || message.Sequence != sequence || message.StreamID != DefaultStreamID {
			t.Fatalf("ParseEvent = %#v, %v", message, err)
		}
		if strings.Contains(string(records[0].Frame), canary) {
			t.Fatal("credential canary reached persisted event")
		}
	}
	pending, err := local.Pending(context.Background(), 10)
	if err != nil || len(pending) != len(events) {
		t.Fatalf("outbox = %#v, %v", pending, err)
	}
	for index, item := range pending {
		if !bytes.Equal(item.Frame, mustReplay(t, local, int64(index))[0].Frame) {
			t.Fatal("event log and outbox frames differ")
		}
	}
	approvals, err := local.PendingApprovals(context.Background(), "thread")
	if err != nil || len(approvals) != 1 || approvals[0].OperationDigest == "" {
		t.Fatalf("pending approvals = %#v, %v", approvals, err)
	}
}

func TestFileChangesFanOutAndCursorRecovery(t *testing.T) {
	manager, _ := newTestManager(t, 1<<20)
	changes := []any{
		map[string]any{"path": "src/a.go", "kind": "add"},
		map[string]any{"path": "src/b.go", "kind": "delete"},
	}
	records, err := manager.Publish(context.Background(), adapter.AgentEvent{Type: protocol.EventFileChanged, WorkspaceID: "workspace", ThreadID: "thread", TurnID: "turn", ItemID: "item", Payload: map[string]any{"changes": changes}})
	if err != nil || len(records) != 2 || records[0].Sequence != 1 || records[1].Sequence != 2 {
		t.Fatalf("file fanout = %#v, %v", records, err)
	}
	if err := manager.Acknowledge(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	batch, err := manager.Replay(context.Background(), 1, 10)
	if err != nil || len(batch.Records) != 1 || batch.Records[0].Sequence != 2 {
		t.Fatalf("Replay = %#v, %v", batch, err)
	}

	large := strings.Repeat("x", (256<<10)+100)
	for index := 0; index < 5; index++ {
		published, err := manager.Publish(context.Background(), adapter.AgentEvent{Type: protocol.EventAgentMessageCompleted, WorkspaceID: "workspace", ThreadID: "thread", TurnID: "turn", ItemID: "message", Payload: map[string]any{"text": large}})
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := protocol.ParseEvent(published[0].Frame)
		if err != nil || parsed.Payload["truncated"] != true {
			t.Fatalf("truncated event = %#v, %v", parsed.Payload, err)
		}
	}
	batch, err = manager.Replay(context.Background(), 0, 10)
	if err != nil || !batch.Gap || batch.EarliestSequence <= 1 {
		t.Fatalf("bounded gap = %#v, %v", batch, err)
	}
	runtime := &snapshotRuntime{snapshot: adapter.ThreadSnapshot{Thread: adapter.Thread{ID: "thread", WorkspaceID: "workspace", Status: "idle"}, Turns: []adapter.Turn{{ID: "turn", ThreadID: "thread", Status: "completed"}}}}
	recovered, err := manager.Recover(context.Background(), runtime, SnapshotTarget{WorkspaceID: "workspace", ThreadID: "thread"}, 0, 10)
	if err != nil || !recovered.Gap || len(recovered.Records) != 2 {
		t.Fatalf("Recover = %#v, %v", recovered, err)
	}
	gap, _ := protocol.ParseEvent(recovered.Records[0].Frame)
	snapshot, _ := protocol.ParseEvent(recovered.Records[1].Frame)
	if gap.Type != string(protocol.EventHistoryGap) || snapshot.Type != string(protocol.EventThreadSnapshot) || snapshot.Payload["status"] != "idle" {
		t.Fatalf("recovery events = %#v / %#v", gap, snapshot)
	}
}

func TestReconcileConfirmsTerminalAndMarksUnknownAmbiguous(t *testing.T) {
	manager, local := newTestManager(t, 16<<20)
	terminal := store.RuntimeThreadRecord{Adapter: "codex", ThreadID: "thread-terminal", WorkspaceID: "workspace", Ownership: "created", State: store.RuntimeThreadNeedsReconcile, ActiveTurnID: "turn-terminal"}
	unknown := store.RuntimeThreadRecord{Adapter: "codex", ThreadID: "thread-unknown", WorkspaceID: "workspace", Ownership: "created", State: store.RuntimeThreadNeedsReconcile, ActiveTurnID: "turn-unknown"}
	for _, record := range []store.RuntimeThreadRecord{terminal, unknown} {
		if err := local.SaveRuntimeThread(context.Background(), record); err != nil {
			t.Fatal(err)
		}
	}
	runtime := &snapshotRuntime{snapshots: map[string]adapter.ThreadSnapshot{
		"thread-terminal": {Thread: adapter.Thread{ID: "thread-terminal", WorkspaceID: "workspace", Status: "notLoaded"}, Turns: []adapter.Turn{{ID: "turn-terminal", ThreadID: "thread-terminal", Status: "completed"}}},
		"thread-unknown":  {Thread: adapter.Thread{ID: "thread-unknown", WorkspaceID: "workspace", Status: "notLoaded"}, Turns: []adapter.Turn{{ID: "turn-unknown", ThreadID: "thread-unknown", Status: "inProgress"}}},
	}}
	report, err := manager.Reconcile(context.Background(), runtime)
	if err != nil || report.Confirmed != 1 || report.Ambiguous != 1 || report.Deferred != 0 {
		t.Fatalf("Reconcile = %#v, %v", report, err)
	}
	for _, id := range []string{"thread-terminal", "thread-unknown"} {
		record, err := local.RuntimeThread(context.Background(), id)
		if err != nil || record.State != store.RuntimeThreadIdle || record.ActiveTurnID != "" {
			t.Fatalf("thread %s = %#v, %v", id, record, err)
		}
	}
	records, _, err := local.ReplayEvents(context.Background(), manager.binding, 0, 10)
	if err != nil || len(records) != 2 || records[0].Type != string(protocol.EventTurnCompleted) || records[1].Type != string(protocol.EventThreadSnapshot) {
		t.Fatalf("reconcile records = %#v, %v", records, err)
	}
}

func TestInvalidEventAndErrorsDoNotExposePayload(t *testing.T) {
	manager, _ := newTestManager(t, 16<<20)
	canary := "event-private-canary"
	_, err := manager.Publish(context.Background(), adapter.AgentEvent{Type: protocol.EventAgentMessageDelta, Payload: map[string]any{"unexpected": canary}})
	if !errors.Is(err, ErrInvalid) || strings.Contains(err.Error(), canary) {
		t.Fatalf("unsafe invalid event error = %v", err)
	}
}

func TestControlResultLifecycleIsIdempotent(t *testing.T) {
	manager, _ := newTestManager(t, 16<<20)
	private := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x31}, ed25519.SeedSize))
	trust := protocol.NewMemoryTrustStore()
	ref := protocol.KeyRef{OwnerID: "owner", NodeID: "node", ClientID: "client", KeyID: "key"}
	if err := trust.Set(ref, protocol.TrustedKey{PublicKey: private.Public().(ed25519.PublicKey), Status: protocol.TrustStatusActive}); err != nil {
		t.Fatal(err)
	}
	validator, err := protocol.NewValidator(protocol.Options{TrustStore: trust, ReplayStore: protocol.NewMemoryReplayStore(), Now: func() time.Time { return eventNow }})
	if err != nil {
		t.Fatal(err)
	}
	expires := eventNow.Add(time.Minute).Format(time.RFC3339Nano)
	nonce := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x19}, 16))
	message := protocol.YuanshuMessage{
		ProtocolVersion: protocol.CurrentVersion, MessageID: "control-1", Type: string(protocol.ControlTurnInterrupt), SentAt: eventNow.Format(time.RFC3339Nano), ExpiresAt: &expires,
		OwnerID: "owner", NodeID: "node", WorkspaceID: pointer("workspace"), ThreadID: pointer("thread"), TurnID: pointer("turn"), StreamID: "client-controls", Sequence: 1,
		CorrelationID: "client-request", Nonce: &nonce, Signer: &protocol.Signer{ClientID: "client", KeyID: "key"}, Payload: map[string]any{},
	}
	input, err := protocol.ControlSigningInput(message)
	if err != nil {
		t.Fatal(err)
	}
	signature := base64.RawURLEncoding.EncodeToString(ed25519.Sign(private, input))
	message.Signature = &signature
	raw, _ := json.Marshal(message)
	validated, err := validator.Validate(context.Background(), raw, protocol.Target{OwnerID: "owner", NodeID: "node"})
	if err != nil {
		t.Fatal(err)
	}
	created, err := manager.BeginControl(context.Background(), validated)
	if err != nil || created.State != store.ControlValidated {
		t.Fatalf("BeginControl = %#v, %v", created, err)
	}
	if _, err := manager.MarkDispatching(context.Background(), message.MessageID); err != nil {
		t.Fatal(err)
	}
	result, err := manager.CompleteControl(context.Background(), message.MessageID, protocol.ControlResultAmbiguous, protocol.ErrorAmbiguous, "confirmation was lost")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := protocol.ParseEvent(result.Frame)
	if err != nil || parsed.Type != string(protocol.EventControlResult) || parsed.CorrelationID != message.MessageID || parsed.Payload["status"] != string(protocol.ControlResultAmbiguous) {
		t.Fatalf("control result = %#v, %v", parsed, err)
	}
	again, err := manager.CompleteControl(context.Background(), message.MessageID, protocol.ControlResultAmbiguous, protocol.ErrorAmbiguous, "ignored duplicate")
	if err != nil || again.Sequence != result.Sequence || !bytes.Equal(again.Frame, result.Frame) {
		t.Fatalf("idempotent result = %#v, %v", again, err)
	}
	if _, err := manager.CompleteControl(context.Background(), message.MessageID, protocol.ControlResultConfirmed, "", ""); !errors.Is(err, ErrControlFinalized) {
		t.Fatalf("terminal rewrite error = %v", err)
	}
}

func newTestManager(t *testing.T, maxBytes int64) (*Manager, *store.Store) {
	t.Helper()
	local, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "node.db"), store.Options{Clock: func() time.Time { return eventNow }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = local.Close() })
	manager, err := NewManager(local, Options{OwnerID: "owner", NodeID: "node", MaxAge: 24 * time.Hour, MaxBytes: maxBytes, Clock: func() time.Time { return eventNow }, Random: &incrementingReader{}})
	if err != nil {
		t.Fatal(err)
	}
	return manager, local
}

func mustReplay(t *testing.T, local *store.Store, after int64) []store.EventRecord {
	t.Helper()
	records, _, err := local.ReplayEvents(context.Background(), testBinding(), after, 1)
	if err != nil || len(records) != 1 {
		t.Fatalf("ReplayEvents = %#v, %v", records, err)
	}
	return records
}

func testBinding() store.EventBinding {
	return store.EventBinding{OwnerID: "owner", NodeID: "node", StreamID: DefaultStreamID}
}

type incrementingReader struct{ next byte }

func (r *incrementingReader) Read(value []byte) (int, error) {
	for index := range value {
		r.next++
		value[index] = r.next
	}
	return len(value), nil
}

type snapshotRuntime struct {
	snapshot  adapter.ThreadSnapshot
	snapshots map[string]adapter.ThreadSnapshot
}

func (r *snapshotRuntime) ReadThread(_ context.Context, request adapter.ReadThreadRequest) (adapter.ThreadSnapshot, error) {
	if r.snapshots != nil {
		value, ok := r.snapshots[request.ThreadID]
		if !ok {
			return adapter.ThreadSnapshot{}, adapter.ErrNotFound
		}
		return value, nil
	}
	return r.snapshot, nil
}
func (*snapshotRuntime) ListThreads(context.Context, adapter.ListThreadsRequest) (adapter.ThreadPage, error) {
	return adapter.ThreadPage{}, adapter.ErrUnsupported
}
func (*snapshotRuntime) StartThread(context.Context, adapter.StartThreadRequest) (adapter.Thread, error) {
	return adapter.Thread{}, adapter.ErrUnsupported
}
func (*snapshotRuntime) ResumeThread(context.Context, adapter.ResumeThreadRequest) (adapter.Thread, error) {
	return adapter.Thread{}, adapter.ErrUnsupported
}
func (*snapshotRuntime) StartTurn(context.Context, adapter.StartTurnRequest) (adapter.Turn, error) {
	return adapter.Turn{}, adapter.ErrUnsupported
}
func (*snapshotRuntime) SteerTurn(context.Context, adapter.SteerTurnRequest) error {
	return adapter.ErrUnsupported
}
func (*snapshotRuntime) InterruptTurn(context.Context, adapter.InterruptTurnRequest) error {
	return adapter.ErrUnsupported
}
func (*snapshotRuntime) ResolveApproval(context.Context, adapter.ApprovalDecision) error {
	return adapter.ErrUnsupported
}
func (*snapshotRuntime) Events() <-chan adapter.AgentEvent { return make(chan adapter.AgentEvent) }
func (*snapshotRuntime) Health() adapter.HealthStatus      { return adapter.HealthStatus{State: "ready"} }
func (*snapshotRuntime) Close(context.Context) error       { return nil }
