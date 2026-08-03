package node

import (
	"context"
	"errors"
	"sync"

	"github.com/yuanshu-ai/yuanshu/internal/adapter"
	"github.com/yuanshu-ai/yuanshu/internal/node/eventlog"
	"github.com/yuanshu-ai/yuanshu/internal/node/store"
	protocol "github.com/yuanshu-ai/yuanshu/internal/protocol/v1"
	"github.com/yuanshu-ai/yuanshu/internal/transport"
)

var ErrControlSessionInvalid = errors.New("node control session configuration is invalid")

type controlWorkspaceStore interface {
	Workspaces(context.Context) ([]store.WorkspaceRecord, error)
	Approval(context.Context, string) (store.ApprovalRecord, error)
}

// ControlSessionOptions binds one authenticated transport to the Node's
// protocol validator, durable event log, workspace policy, and Agent runtime.
// The same component is used by Relay and Standalone compositions.
type ControlSessionOptions struct {
	Transport    transport.Transport
	Validator    *protocol.Validator
	Target       protocol.Target
	Events       *eventlog.Manager
	Store        controlWorkspaceStore
	Runtime      adapter.Runtime
	DeviceName   string
	RefreshTrust func(context.Context) error
	// EventFailure is called once when the persistent event pump can no longer
	// append Runtime.Events to the local event log. It is intentionally a
	// callback so the host can make the remote control state unavailable
	// without coupling this protocol boundary to host lifecycle code.
	EventFailure func(error)
}

// ControlSession is the formal Node-side Protocol v1 control boundary.
type ControlSession struct {
	validator    *protocol.Validator
	target       protocol.Target
	events       *eventlog.Manager
	store        controlWorkspaceStore
	runtime      adapter.Runtime
	deviceName   string
	refreshTrust func(context.Context) error
	eventFailure func(error)

	transportMu sync.RWMutex
	active      *sessionTransport
	deliveryMu  sync.Mutex

	lifecycleMu sync.Mutex
	started     bool
	closed      bool
	eventCancel context.CancelFunc
	eventDone   chan struct{}
	eventErr    error
}

type sessionTransport struct {
	value transport.Transport
}

func NewControlSession(options ControlSessionOptions) (*ControlSession, error) {
	if options.Validator == nil || options.Events == nil || options.Store == nil || options.Runtime == nil ||
		options.Target.OwnerID == "" || options.Target.NodeID == "" {
		return nil, ErrControlSessionInvalid
	}
	return &ControlSession{
		validator: options.Validator, target: options.Target,
		events: options.Events, store: options.Store, runtime: options.Runtime, deviceName: options.DeviceName, refreshTrust: options.RefreshTrust,
		active: func() *sessionTransport {
			if options.Transport == nil {
				return nil
			}
			return &sessionTransport{value: options.Transport}
		}(),
		eventFailure: options.EventFailure,
	}, nil
}

// Start begins the persistent Runtime event pump. The pump is deliberately
// independent from any one Relay connection so a disconnected Node keeps
// draining Runtime.Events into the durable local event log.
func (s *ControlSession) Start(ctx context.Context) error {
	if ctx == nil {
		return context.Canceled
	}
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.closed {
		return transport.ErrClosed
	}
	if s.started {
		return nil
	}
	eventCtx, cancel := context.WithCancel(ctx)
	s.eventCancel = cancel
	s.eventDone = make(chan struct{})
	s.started = true
	go s.runEventPump(eventCtx)
	return nil
}

// Serve receives immutable raw control frames on one authenticated transport.
// The same ControlSession may serve multiple sequential transports.
func (s *ControlSession) Serve(ctx context.Context, endpoint transport.Transport) error {
	if ctx == nil {
		return context.Canceled
	}
	if endpoint == nil {
		return errNoTransport
	}
	if err := s.Start(ctx); err != nil {
		return err
	}
	binding := s.bind(endpoint)
	defer func() {
		s.unbind(binding)
		_ = endpoint.Close()
	}()
	for {
		frame, err := endpoint.Receive(ctx)
		if err != nil {
			if errors.Is(err, transport.ErrClosed) || errors.Is(err, context.Canceled) && ctx.Err() != nil {
				return nil
			}
			return errors.New("node control transport failed")
		}
		if err := s.handle(ctx, frame.Bytes()); err != nil {
			if errors.Is(err, errNoTransport) || errors.Is(err, transport.ErrClosed) {
				return nil
			}
			return err
		}
	}
}

// Close stops the persistent event pump and the currently bound transport.
// Runtime and the local Store remain owned by the caller.
func (s *ControlSession) Close() error {
	s.lifecycleMu.Lock()
	if s.closed {
		s.lifecycleMu.Unlock()
		return nil
	}
	s.closed = true
	cancel := s.eventCancel
	done := s.eventDone
	s.eventCancel = nil
	s.lifecycleMu.Unlock()
	if cancel != nil {
		cancel()
	}
	if active := s.current(); active != nil {
		_ = active.Close()
	}
	if done != nil {
		<-done
	}
	return nil
}

// Run preserves the original one-transport API used by Standalone and tests.
// Node reconnect callers use Start and Serve separately.
func (s *ControlSession) Run(ctx context.Context) error {
	if ctx == nil {
		return context.Canceled
	}
	if err := s.Start(ctx); err != nil {
		return err
	}
	defer s.Close()
	endpoint := s.current()
	if endpoint == nil {
		return errNoTransport
	}
	return s.Serve(ctx, endpoint)
}

var errNoTransport = errors.New("node control transport is unavailable")

func (s *ControlSession) bind(endpoint transport.Transport) *sessionTransport {
	binding := &sessionTransport{value: endpoint}
	s.transportMu.Lock()
	s.active = binding
	s.transportMu.Unlock()
	return binding
}

func (s *ControlSession) unbind(binding *sessionTransport) {
	s.transportMu.Lock()
	if s.active == binding {
		s.active = nil
	}
	s.transportMu.Unlock()
}

func (s *ControlSession) current() transport.Transport {
	s.transportMu.RLock()
	defer s.transportMu.RUnlock()
	if s.active == nil {
		return nil
	}
	return s.active.value
}

func (s *ControlSession) runEventPump(ctx context.Context) {
	defer close(s.eventDone)
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-s.runtime.Events():
			if !ok {
				return
			}
			if err := s.publish(ctx, event); err != nil {
				s.lifecycleMu.Lock()
				s.eventErr = err
				s.lifecycleMu.Unlock()
				if s.eventFailure != nil {
					s.eventFailure(err)
				}
				return
			}
		}
	}
}

func (s *ControlSession) handle(ctx context.Context, raw []byte) error {
	validated, err := s.validator.Validate(ctx, raw, s.target)
	var validation *protocol.ValidationError
	if err != nil && s.refreshTrust != nil && errors.As(err, &validation) && validation.Stage == protocol.ValidationStageTrust && validation.Code == protocol.ErrorUnauthorized {
		if refreshErr := s.refreshTrust(ctx); refreshErr == nil {
			validated, err = s.validator.Validate(ctx, raw, s.target)
		}
	}
	if err != nil {
		return err
	}
	message := validated.Message()
	if _, err := s.events.BeginControl(ctx, validated); err != nil {
		return errors.New("node control persistence failed")
	}
	if _, err := s.events.MarkDispatching(ctx, message.MessageID); err != nil {
		return errors.New("node control persistence failed")
	}
	if protocol.ControlType(message.Type) == protocol.ControlEventsReplay {
		return s.handleReplay(ctx, message)
	}
	status, code := protocol.ControlResultConfirmed, protocol.ErrorCode("")
	if err := s.dispatch(ctx, message); err != nil {
		status, code = classifyControlFailure(err)
	}
	result, err := s.events.CompleteControl(ctx, message.MessageID, status, code, "")
	if err != nil {
		return errors.New("node control result persistence failed")
	}
	return s.send(ctx, result.Frame)
}

func (s *ControlSession) handleReplay(ctx context.Context, message protocol.YuanshuMessage) error {
	// Hold the delivery read/write gate for the complete replay batch and its
	// control.result. Runtime events are still persisted while this lock is held
	// but cannot be inserted between replay records.
	s.deliveryMu.Lock()
	defer s.deliveryMu.Unlock()
	batch, replayErr := s.events.Replay(ctx, int64Payload(message.Payload, "afterSequence"), eventlog.DefaultReplayLimit)
	status, code := protocol.ControlResultConfirmed, protocol.ErrorCode("")
	if replayErr == nil && batch.Gap {
		workspaceID, threadID := value(message.WorkspaceID), value(message.ThreadID)
		if workspaceID == "" || threadID == "" {
			replayErr = eventlog.ErrHistoryGap
		} else {
			batch, replayErr = s.events.Recover(ctx, s.runtime, eventlog.SnapshotTarget{WorkspaceID: workspaceID, ThreadID: threadID}, int64Payload(message.Payload, "afterSequence"), eventlog.DefaultReplayLimit)
		}
	}
	if replayErr != nil {
		status, code = classifyControlFailure(replayErr)
	} else {
		for _, record := range batch.Records {
			if err := s.sendFrameLocked(ctx, record.Frame); err != nil && !errors.Is(err, errNoTransport) {
				status, code = protocol.ControlResultRejected, protocol.ErrorRuntimeUnavailable
				break
			}
		}
	}
	result, err := s.events.CompleteControl(ctx, message.MessageID, status, code, "")
	if err != nil {
		return errors.New("node control result persistence failed")
	}
	if sendErr := s.sendFrameLocked(ctx, result.Frame); sendErr != nil {
		return sendErr
	}
	return nil
}

func (s *ControlSession) dispatch(ctx context.Context, message protocol.YuanshuMessage) error {
	workspaceID, threadID, turnID, itemID := value(message.WorkspaceID), value(message.ThreadID), value(message.TurnID), value(message.ItemID)
	switch protocol.ControlType(message.Type) {
	case protocol.ControlDeviceSync:
		health := s.runtime.Health()
		return s.publish(ctx, adapter.AgentEvent{Type: protocol.EventDeviceStatus, CorrelationID: message.MessageID, Payload: map[string]any{
			"status": "online", "name": s.deviceName, "runtime": health.State,
		}})
	case protocol.ControlWorkspaceList:
		items, err := s.store.Workspaces(ctx)
		if err != nil {
			return adapter.ErrUnavailable
		}
		workspaces := make([]any, 0, len(items))
		for _, item := range items {
			workspaces = append(workspaces, map[string]any{"id": item.ID, "name": item.DisplayName, "adapter": item.Adapter, "permissionProfile": item.PermissionProfile})
		}
		return s.publish(ctx, adapter.AgentEvent{Type: protocol.EventDeviceStatus, CorrelationID: message.MessageID, Payload: map[string]any{"status": "online", "workspaces": workspaces}})
	case protocol.ControlThreadList:
		if workspaceID == "" {
			return adapter.ErrInvalid
		}
		page, err := s.runtime.ListThreads(ctx, adapter.ListThreadsRequest{WorkspaceID: workspaceID, Cursor: stringPayload(message.Payload, "cursor"), Limit: intPayload(message.Payload, "limit")})
		if err != nil {
			return err
		}
		threads := make([]any, 0, len(page.Data))
		for _, thread := range page.Data {
			threads = append(threads, map[string]any{"id": thread.ID, "status": thread.Status})
		}
		return s.publish(ctx, adapter.AgentEvent{Type: protocol.EventThreadSnapshot, CorrelationID: message.MessageID, WorkspaceID: workspaceID, Payload: map[string]any{"status": "listed", "threads": threads, "nextCursor": page.NextCursor}})
	case protocol.ControlThreadRead:
		if workspaceID == "" || threadID == "" {
			return adapter.ErrInvalid
		}
		snapshot, err := s.runtime.ReadThread(ctx, adapter.ReadThreadRequest{WorkspaceID: workspaceID, ThreadID: threadID, IncludeTurns: boolPayload(message.Payload, "includeTurns")})
		if err != nil {
			return err
		}
		return s.publishSnapshot(ctx, message.MessageID, snapshot)
	case protocol.ControlThreadStart:
		if workspaceID == "" {
			return adapter.ErrInvalid
		}
		thread, err := s.runtime.StartThread(ctx, adapter.StartThreadRequest{WorkspaceID: workspaceID})
		if err != nil {
			return err
		}
		if err := s.publish(ctx, adapter.AgentEvent{Type: protocol.EventThreadStarted, CorrelationID: message.MessageID, WorkspaceID: workspaceID, ThreadID: thread.ID, Payload: map[string]any{"status": thread.Status}}); err != nil {
			return err
		}
		turn, err := s.runtime.StartTurn(ctx, adapter.StartTurnRequest{WorkspaceID: workspaceID, ThreadID: thread.ID, Input: stringPayload(message.Payload, "input")})
		if err != nil {
			return err
		}
		return s.publish(ctx, adapter.AgentEvent{Type: protocol.EventTurnStarted, CorrelationID: message.MessageID, WorkspaceID: workspaceID, ThreadID: thread.ID, TurnID: turn.ID, Payload: map[string]any{"status": turn.Status}})
	case protocol.ControlThreadResume:
		if workspaceID == "" || threadID == "" {
			return adapter.ErrInvalid
		}
		thread, err := s.runtime.ResumeThread(ctx, adapter.ResumeThreadRequest{WorkspaceID: workspaceID, ThreadID: threadID})
		if err != nil {
			return err
		}
		return s.publish(ctx, adapter.AgentEvent{Type: protocol.EventThreadStarted, CorrelationID: message.MessageID, WorkspaceID: workspaceID, ThreadID: thread.ID, Payload: map[string]any{"status": "resumed"}})
	case protocol.ControlTurnStart:
		if workspaceID == "" || threadID == "" {
			return adapter.ErrInvalid
		}
		turn, err := s.runtime.StartTurn(ctx, adapter.StartTurnRequest{WorkspaceID: workspaceID, ThreadID: threadID, Input: stringPayload(message.Payload, "input")})
		if err != nil {
			return err
		}
		return s.publish(ctx, adapter.AgentEvent{Type: protocol.EventTurnStarted, CorrelationID: message.MessageID, WorkspaceID: workspaceID, ThreadID: threadID, TurnID: turn.ID, Payload: map[string]any{"status": turn.Status}})
	case protocol.ControlTurnSteer:
		if workspaceID == "" || threadID == "" || turnID == "" {
			return adapter.ErrInvalid
		}
		return s.runtime.SteerTurn(ctx, adapter.SteerTurnRequest{WorkspaceID: workspaceID, ThreadID: threadID, TurnID: turnID, Input: stringPayload(message.Payload, "input")})
	case protocol.ControlTurnInterrupt:
		if workspaceID == "" || threadID == "" || turnID == "" {
			return adapter.ErrInvalid
		}
		return s.runtime.InterruptTurn(ctx, adapter.InterruptTurnRequest{WorkspaceID: workspaceID, ThreadID: threadID, TurnID: turnID})
	case protocol.ControlApprovalResolve:
		approvalID := stringPayload(message.Payload, "approvalId")
		approval, err := s.store.Approval(ctx, approvalID)
		if err != nil || approval.Status != store.ApprovalPending || approval.OperationDigest != stringPayload(message.Payload, "operationDigest") ||
			approval.WorkspaceID != workspaceID || approval.ThreadID != threadID || approval.TurnID != turnID || approval.ItemID != itemID {
			return adapter.ErrConflict
		}
		return s.runtime.ResolveApproval(ctx, adapter.ApprovalDecision{WorkspaceID: workspaceID, ThreadID: threadID, TurnID: turnID, ItemID: itemID, ApprovalID: approvalID, Decision: stringPayload(message.Payload, "decision")})
	case protocol.ControlEventsReplay:
		batch, err := s.events.Replay(ctx, int64Payload(message.Payload, "afterSequence"), eventlog.DefaultReplayLimit)
		if err != nil {
			return err
		}
		for _, record := range batch.Records {
			if err := s.send(ctx, record.Frame); err != nil {
				return err
			}
		}
		return nil
	case protocol.ControlSnapshotRequest:
		if workspaceID == "" || threadID == "" {
			return adapter.ErrInvalid
		}
		record, err := s.events.Snapshot(ctx, s.runtime, eventlog.SnapshotTarget{WorkspaceID: workspaceID, ThreadID: threadID})
		if err != nil {
			return err
		}
		return s.send(ctx, record.Frame)
	default:
		return adapter.ErrUnsupported
	}
}

func (s *ControlSession) publishSnapshot(ctx context.Context, correlationID string, snapshot adapter.ThreadSnapshot) error {
	turns := make([]any, 0, len(snapshot.Turns))
	for _, turn := range snapshot.Turns {
		turns = append(turns, map[string]any{"id": turn.ID, "status": turn.Status})
	}
	return s.publish(ctx, adapter.AgentEvent{Type: protocol.EventThreadSnapshot, CorrelationID: correlationID, WorkspaceID: snapshot.Thread.WorkspaceID, ThreadID: snapshot.Thread.ID, Payload: map[string]any{"status": snapshot.Thread.Status, "turns": turns}})
}

func (s *ControlSession) publish(ctx context.Context, event adapter.AgentEvent) error {
	records, err := s.events.Publish(ctx, event)
	if err != nil {
		return errors.New("node event persistence failed")
	}
	for _, record := range records {
		_ = s.sendFrame(ctx, record.Frame)
	}
	return nil
}

func (s *ControlSession) sendFrame(ctx context.Context, raw []byte) error {
	s.deliveryMu.Lock()
	defer s.deliveryMu.Unlock()
	return s.sendFrameLocked(ctx, raw)
}

// send preserves the small internal helper used by dispatch paths while all
// outbound frames now pass through the delivery serializer.
func (s *ControlSession) send(ctx context.Context, raw []byte) error {
	return s.sendFrame(ctx, raw)
}

func (s *ControlSession) sendFrameLocked(ctx context.Context, raw []byte) error {
	s.transportMu.RLock()
	binding := s.active
	s.transportMu.RUnlock()
	if binding == nil || binding.value == nil {
		return errNoTransport
	}
	if err := binding.value.Send(ctx, transport.NewFrame(raw)); err != nil {
		s.transportMu.Lock()
		if s.active == binding {
			s.active = nil
		}
		s.transportMu.Unlock()
		if errors.Is(err, transport.ErrClosed) || errors.Is(err, transport.ErrBackpressure) {
			return errNoTransport
		}
		return errors.New("node event transport failed")
	}
	return nil
}

func classifyControlFailure(err error) (protocol.ControlResultStatus, protocol.ErrorCode) {
	switch {
	case errors.Is(err, adapter.ErrAmbiguous), errors.Is(err, adapter.ErrReconciliationNeeded):
		return protocol.ControlResultAmbiguous, protocol.ErrorAmbiguous
	case errors.Is(err, adapter.ErrInvalid):
		return protocol.ControlResultRejected, protocol.ErrorInvalidMessage
	case errors.Is(err, adapter.ErrForbidden):
		return protocol.ControlResultRejected, protocol.ErrorForbidden
	case errors.Is(err, adapter.ErrNotFound), errors.Is(err, store.ErrNotFound):
		return protocol.ControlResultRejected, protocol.ErrorNotFound
	case errors.Is(err, adapter.ErrConflict):
		return protocol.ControlResultRejected, protocol.ErrorConflict
	case errors.Is(err, adapter.ErrUnsupported):
		return protocol.ControlResultRejected, protocol.ErrorUnsupportedControl
	case errors.Is(err, eventlog.ErrHistoryGap):
		return protocol.ControlResultRejected, protocol.ErrorHistoryGap
	case errors.Is(err, eventlog.ErrSequenceExhausted):
		return protocol.ControlResultRejected, protocol.ErrorConflict
	case errors.Is(err, adapter.ErrUnavailable), errors.Is(err, adapter.ErrClosed):
		return protocol.ControlResultRejected, protocol.ErrorRuntimeUnavailable
	default:
		return protocol.ControlResultRejected, protocol.ErrorRuntimeFailed
	}
}

func value(pointer *string) string {
	if pointer == nil {
		return ""
	}
	return *pointer
}

func stringPayload(payload map[string]any, key string) string {
	value, _ := payload[key].(string)
	return value
}

func boolPayload(payload map[string]any, key string) bool {
	value, _ := payload[key].(bool)
	return value
}

func intPayload(payload map[string]any, key string) int {
	return int(int64Payload(payload, key))
}

func int64Payload(payload map[string]any, key string) int64 {
	value, _ := payload[key].(float64)
	return int64(value)
}
