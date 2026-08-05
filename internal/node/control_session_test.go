package node

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/yuanshu-ai/yuanshu/internal/adapter"
	"github.com/yuanshu-ai/yuanshu/internal/node/eventlog"
	"github.com/yuanshu-ai/yuanshu/internal/node/store"
	protocol "github.com/yuanshu-ai/yuanshu/internal/protocol/v1"
	protocolv11 "github.com/yuanshu-ai/yuanshu/internal/protocol/v11"
	"github.com/yuanshu-ai/yuanshu/internal/transport"
)

var controlSessionNow = time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)

func TestControlSessionValidatesDispatchesAndReturnsDurableEvents(t *testing.T) {
	serverSide, session, runtime, private, _ := newControlSessionHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- session.Run(ctx) }()

	raw := signedSessionControl(t, private, protocol.ControlDeviceSync, 1, map[string]any{}, nil, nil, nil, nil)
	if err := serverSide.Send(ctx, transport.NewFrame(raw)); err != nil {
		t.Fatal(err)
	}
	first := receiveSessionEvent(t, serverSide)
	second := receiveSessionEvent(t, serverSide)
	if first.Type != string(protocol.EventDeviceStatus) || first.Payload["runtime"] != "ready" {
		t.Fatalf("device event = %#v", first)
	}
	if second.Type != string(protocol.EventControlResult) || second.CorrelationID != "message-1" || second.Payload["status"] != string(protocol.ControlResultConfirmed) {
		t.Fatalf("control result = %#v", second)
	}
	if runtime.calls != 0 {
		t.Fatalf("runtime calls = %d", runtime.calls)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run() = %v", err)
	}
}

func TestControlSessionThreadStartUsesAdapterBoundary(t *testing.T) {
	serverSide, session, runtime, private, _ := newControlSessionHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- session.Run(ctx) }()
	workspace := "workspace"
	raw := signedSessionControl(t, private, protocol.ControlThreadStart, 1, map[string]any{"input": "synthetic input"}, &workspace, nil, nil, nil)
	if err := serverSide.Send(ctx, transport.NewFrame(raw)); err != nil {
		t.Fatal(err)
	}
	for _, want := range []protocol.EventType{protocol.EventThreadStarted, protocol.EventTurnStarted, protocol.EventControlResult} {
		message := receiveSessionEvent(t, serverSide)
		if message.Type != string(want) {
			t.Fatalf("event = %q, want %q", message.Type, want)
		}
	}
	if runtime.calls != 2 || runtime.lastInput != "synthetic input" {
		t.Fatalf("runtime calls/input = %d/%q", runtime.calls, runtime.lastInput)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run() = %v", err)
	}
}

func TestControlSessionProtocol11UsesOpaqueTaskSurfaceAndIndependentStream(t *testing.T) {
	serverSide, session, runtime, private, local := newControlSessionHarness(t)
	managerV11, err := eventlog.NewManager(local, eventlog.Options{OwnerID: "owner", NodeID: "node", ProtocolVersion: protocolv11.CurrentVersion, MaxAge: time.Hour, MaxBytes: 16 << 20, Clock: func() time.Time { return controlSessionNow }})
	if err != nil {
		t.Fatal(err)
	}
	validatorV11, err := protocolv11.NewValidator(protocolv11.Options{TrustStore: local, ReplayStore: local, Now: func() time.Time { return controlSessionNow }})
	if err != nil {
		t.Fatal(err)
	}
	session.validatorV11, session.eventsV11 = validatorV11, managerV11
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- session.Run(ctx) }()

	if err := serverSide.Send(ctx, transport.NewFrame(signedSessionControlV11(t, private, protocolv11.ControlTaskStart, 1, map[string]any{"input": "synthetic input"}, "codex-default", "workspace", "", "", ""))); err != nil {
		t.Fatal(err)
	}
	want := []protocolv11.EventType{protocolv11.EventTaskStarted, protocolv11.EventRunStarted, protocolv11.EventControlResult}
	for _, expected := range want {
		for {
			ctxReceive, stop := context.WithTimeout(context.Background(), 2*time.Second)
			frame, receiveErr := serverSide.Receive(ctxReceive)
			stop()
			if receiveErr != nil {
				t.Fatal(receiveErr)
			}
			message, parseErr := protocolv11.ParseEvent(frame.Bytes())
			if parseErr != nil {
				continue
			}
			if protocolv11.EventType(message.Type) != expected || message.StreamID != eventlog.DefaultStreamIDV11 {
				t.Fatalf("event = %q stream=%q, want %q", message.Type, message.StreamID, expected)
			}
			break
		}
	}
	if runtime.calls != 2 || runtime.lastInput != "synthetic input" {
		t.Fatalf("runtime calls/input = %d/%q", runtime.calls, runtime.lastInput)
	}
	runtime.events <- adapter.AgentEvent{Type: protocol.EventType(protocolv11.EventReasoningSummaryDelta), AgentInstanceID: "codex-default", WorkspaceID: "workspace", ThreadID: "thread", TurnID: "turn", ItemID: "reasoning", Payload: map[string]any{"text": "Visible summary"}}
	for {
		ctxReceive, stop := context.WithTimeout(context.Background(), 2*time.Second)
		frame, receiveErr := serverSide.Receive(ctxReceive)
		stop()
		if receiveErr != nil {
			t.Fatal(receiveErr)
		}
		message, parseErr := protocolv11.ParseEvent(frame.Bytes())
		if parseErr == nil && string(message.Type) == string(protocolv11.EventReasoningSummaryDelta) {
			break
		}
	}
	legacy, _, err := local.ReplayEvents(context.Background(), store.EventBinding{OwnerID: "owner", NodeID: "node", StreamID: eventlog.DefaultStreamID}, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range legacy {
		if record.Type == string(protocolv11.EventReasoningSummaryDelta) {
			t.Fatal("Protocol 1.1 reasoning event leaked into the frozen 1.0 stream")
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestControlSessionRejectsTamperedControlBeforeDispatch(t *testing.T) {
	serverSide, session, runtime, private, _ := newControlSessionHarness(t)
	raw := signedSessionControl(t, private, protocol.ControlDeviceSync, 1, map[string]any{}, nil, nil, nil, nil)
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	document["ownerId"] = "other-owner"
	raw, _ = json.Marshal(document)
	done := make(chan error, 1)
	go func() { done <- session.Run(context.Background()) }()
	if err := serverSide.Send(context.Background(), transport.NewFrame(raw)); err != nil {
		t.Fatal(err)
	}
	err := <-done
	var validation *protocol.ValidationError
	if !errors.As(err, &validation) || validation.Code != protocol.ErrorForbidden {
		t.Fatalf("Run() error = %v", err)
	}
	if runtime.calls != 0 {
		t.Fatalf("tampered control reached runtime: %d calls", runtime.calls)
	}
}

func TestControlSessionRefreshesOwnerTrustOnceForUnknownSigner(t *testing.T) {
	local, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "node.db"), store.Options{Clock: func() time.Time { return controlSessionNow }})
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close()
	public, private, _ := ed25519.GenerateKey(nil)
	manager, err := eventlog.NewManager(local, eventlog.Options{OwnerID: "owner", NodeID: "node", MaxAge: time.Hour, MaxBytes: 16 << 20, Clock: func() time.Time { return controlSessionNow }})
	if err != nil {
		t.Fatal(err)
	}
	validator, err := protocol.NewValidator(protocol.Options{TrustStore: local, ReplayStore: local, Now: func() time.Time { return controlSessionNow }})
	if err != nil {
		t.Fatal(err)
	}
	serverSide, nodeSide, _ := transport.NewStandalonePair(transport.StandaloneOptions{QueueCapacity: 8})
	defer serverSide.Close()
	runtime := &controlRuntime{events: make(chan adapter.AgentEvent, 4)}
	refreshes := 0
	session, err := NewControlSession(ControlSessionOptions{Transport: nodeSide, Validator: validator, Target: protocol.Target{OwnerID: "owner", NodeID: "node"}, Events: manager, Store: local, Runtime: runtime, DeviceName: "Node", RefreshTrust: func(ctx context.Context) error {
		refreshes++
		return local.PutTrustedKey(ctx, protocol.KeyRef{OwnerID: "owner", NodeID: "node", ClientID: "client", KeyID: "key"}, protocol.TrustedKey{PublicKey: public, Status: protocol.TrustStatusActive})
	}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- session.Run(ctx) }()
	raw := signedSessionControl(t, private, protocol.ControlDeviceSync, 1, map[string]any{}, nil, nil, nil, nil)
	if err := serverSide.Send(ctx, transport.NewFrame(raw)); err != nil {
		t.Fatal(err)
	}
	_ = receiveSessionEvent(t, serverSide)
	_ = receiveSessionEvent(t, serverSide)
	if refreshes != 1 {
		t.Fatalf("refreshes=%d", refreshes)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestControlSessionWorkspaceListIncludesSafeExecutionPolicy(t *testing.T) {
	serverSide, session, _, private, local := newControlSessionHarness(t)
	if err := local.ReplaceWorkspaces(context.Background(), []store.WorkspaceRecord{{
		ID: "workspace", DisplayName: "Workspace", CanonicalPath: "/private/synthetic/workspace", FilesystemRoot: "/", FileIdentity: "device:file", Adapter: "codex", PermissionProfile: "workspace-write", AllowNetwork: true,
	}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- session.Run(ctx) }()
	if err := serverSide.Send(ctx, transport.NewFrame(signedSessionControl(t, private, protocol.ControlWorkspaceList, 1, map[string]any{}, nil, nil, nil, nil))); err != nil {
		t.Fatal(err)
	}
	event := receiveSessionEvent(t, serverSide)
	if event.Type != string(protocol.EventDeviceStatus) {
		t.Fatalf("event type = %q", event.Type)
	}
	items, ok := event.Payload["workspaces"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("workspaces = %#v", event.Payload["workspaces"])
	}
	workspace, ok := items[0].(map[string]any)
	if !ok || workspace["allowNetwork"] != true || workspace["permissionProfile"] != "workspace-write" {
		t.Fatalf("workspace = %#v", items[0])
	}
	if _, exposed := workspace["canonicalPath"]; exposed {
		t.Fatalf("workspace exposed canonical path: %#v", workspace)
	}
	if _, exposed := workspace["path"]; exposed {
		t.Fatalf("workspace exposed path: %#v", workspace)
	}
	_ = receiveSessionEvent(t, serverSide)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func newControlSessionHarness(t *testing.T) (transport.Transport, *ControlSession, *controlRuntime, ed25519.PrivateKey, *store.Store) {
	t.Helper()
	local, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "node.db"), store.Options{Clock: func() time.Time { return controlSessionNow }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = local.Close() })
	public, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	ref := protocol.KeyRef{OwnerID: "owner", NodeID: "node", ClientID: "client", KeyID: "key"}
	if err := local.PutTrustedKey(context.Background(), ref, protocol.TrustedKey{PublicKey: public, Status: protocol.TrustStatusActive}); err != nil {
		t.Fatal(err)
	}
	manager, err := eventlog.NewManager(local, eventlog.Options{OwnerID: "owner", NodeID: "node", MaxAge: time.Hour, MaxBytes: 16 << 20, Clock: func() time.Time { return controlSessionNow }})
	if err != nil {
		t.Fatal(err)
	}
	validator, err := protocol.NewValidator(protocol.Options{TrustStore: local, ReplayStore: local, Now: func() time.Time { return controlSessionNow }})
	if err != nil {
		t.Fatal(err)
	}
	serverSide, nodeSide, err := transport.NewStandalonePair(transport.StandaloneOptions{QueueCapacity: 16})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = serverSide.Close(); _ = nodeSide.Close() })
	runtime := &controlRuntime{events: make(chan adapter.AgentEvent, 8)}
	session, err := NewControlSession(ControlSessionOptions{
		Transport: nodeSide, Validator: validator, Target: protocol.Target{OwnerID: "owner", NodeID: "node"}, Events: manager, Store: local, Runtime: runtime, DeviceName: "Synthetic Node",
	})
	if err != nil {
		t.Fatal(err)
	}
	return serverSide, session, runtime, private, local
}

func signedSessionControl(t *testing.T, private ed25519.PrivateKey, kind protocol.ControlType, sequence int64, payload map[string]any, workspace, thread, turn, item *string) []byte {
	t.Helper()
	expires := controlSessionNow.Add(time.Minute).Format(time.RFC3339Nano)
	nonce := base64.RawURLEncoding.EncodeToString([]byte("0123456789abcdef"))
	message := protocol.YuanshuMessage{
		ProtocolVersion: protocol.CurrentVersion, MessageID: "message-1", Type: string(kind), SentAt: controlSessionNow.Format(time.RFC3339Nano),
		OwnerID: "owner", NodeID: "node", WorkspaceID: workspace, ThreadID: thread, TurnID: turn, ItemID: item,
		StreamID: "control-stream", Sequence: sequence, CorrelationID: "correlation-1", Payload: payload,
		ExpiresAt: &expires, Nonce: &nonce, Signer: &protocol.Signer{ClientID: "client", KeyID: "key"},
	}
	input, err := protocol.ControlSigningInput(message)
	if err != nil {
		t.Fatal(err)
	}
	signature := base64.RawURLEncoding.EncodeToString(ed25519.Sign(private, input))
	message.Signature = &signature
	raw, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func signedSessionControlV11(t *testing.T, private ed25519.PrivateKey, kind protocolv11.ControlType, sequence int64, payload map[string]any, agentID, workspaceID, taskID, runID, interactionID string) []byte {
	t.Helper()
	expires := controlSessionNow.Add(time.Minute).Format(time.RFC3339Nano)
	nonce := base64.RawURLEncoding.EncodeToString([]byte("fedcba9876543210"))
	message := protocolv11.YuanshuMessage{
		ProtocolVersion: protocolv11.The11, MessageID: "message-v11", Type: protocolv11.Type(kind), SentAt: controlSessionNow.Format(time.RFC3339Nano),
		OwnerID: "owner", NodeID: "node", AgentInstanceID: optionalString(agentID), WorkspaceID: optionalString(workspaceID), TaskID: optionalString(taskID), RunID: optionalString(runID), InteractionID: optionalString(interactionID),
		StreamID: "control-stream", Sequence: sequence, CorrelationID: "correlation-v11", Payload: payload,
		ExpiresAt: &expires, Nonce: &nonce, Signer: &protocolv11.Signer{ClientID: "client", KeyID: "key"},
	}
	input, err := protocolv11.ControlSigningInput(message)
	if err != nil {
		t.Fatal(err)
	}
	signature := base64.RawURLEncoding.EncodeToString(ed25519.Sign(private, input))
	message.Signature = &signature
	raw, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func receiveSessionEvent(t *testing.T, endpoint transport.Transport) protocol.YuanshuMessage {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	frame, err := endpoint.Receive(ctx)
	if err != nil {
		t.Fatal(err)
	}
	message, err := protocol.ParseEvent(frame.Bytes())
	if err != nil {
		t.Fatalf("ParseEvent() = %v", err)
	}
	return message
}

type controlRuntime struct {
	events    chan adapter.AgentEvent
	calls     int
	lastInput string
}

func (r *controlRuntime) ListThreads(context.Context, adapter.ListThreadsRequest) (adapter.ThreadPage, error) {
	r.calls++
	return adapter.ThreadPage{}, nil
}
func (r *controlRuntime) ReadThread(context.Context, adapter.ReadThreadRequest) (adapter.ThreadSnapshot, error) {
	r.calls++
	return adapter.ThreadSnapshot{Thread: adapter.Thread{ID: "thread", WorkspaceID: "workspace", Status: "idle"}}, nil
}
func (r *controlRuntime) StartThread(_ context.Context, request adapter.StartThreadRequest) (adapter.Thread, error) {
	r.calls++
	return adapter.Thread{ID: "thread", WorkspaceID: request.WorkspaceID, Status: "idle"}, nil
}
func (r *controlRuntime) ResumeThread(_ context.Context, request adapter.ResumeThreadRequest) (adapter.Thread, error) {
	r.calls++
	return adapter.Thread{ID: request.ThreadID, WorkspaceID: request.WorkspaceID, Status: "idle"}, nil
}
func (r *controlRuntime) StartTurn(_ context.Context, request adapter.StartTurnRequest) (adapter.Turn, error) {
	r.calls++
	r.lastInput = request.Input
	return adapter.Turn{ID: "turn", ThreadID: request.ThreadID, Status: "inProgress"}, nil
}
func (r *controlRuntime) SteerTurn(context.Context, adapter.SteerTurnRequest) error {
	r.calls++
	return nil
}
func (r *controlRuntime) InterruptTurn(context.Context, adapter.InterruptTurnRequest) error {
	r.calls++
	return nil
}
func (r *controlRuntime) ResolveApproval(context.Context, adapter.ApprovalDecision) error {
	r.calls++
	return nil
}
func (r *controlRuntime) Events() <-chan adapter.AgentEvent { return r.events }
func (r *controlRuntime) Health() adapter.HealthStatus      { return adapter.HealthStatus{State: "ready"} }
func (r *controlRuntime) Close(context.Context) error       { return nil }
