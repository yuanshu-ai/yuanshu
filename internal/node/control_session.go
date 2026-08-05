package node

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/yuanshu-ai/yuanshu/internal/adapter"
	"github.com/yuanshu-ai/yuanshu/internal/node/eventlog"
	"github.com/yuanshu-ai/yuanshu/internal/node/store"
	protocol "github.com/yuanshu-ai/yuanshu/internal/protocol/v1"
	protocolv11 "github.com/yuanshu-ai/yuanshu/internal/protocol/v11"
	"github.com/yuanshu-ai/yuanshu/internal/transport"
)

var ErrControlSessionInvalid = errors.New("node control session configuration is invalid")

type controlWorkspaceStore interface {
	Workspaces(context.Context) ([]store.WorkspaceRecord, error)
	Approval(context.Context, string) (store.ApprovalRecord, error)
	ClaimApproval(context.Context, store.ApprovalClaim) (store.ApprovalRecord, error)
	MarkApprovalAmbiguous(context.Context, string) error
}

type controlAgentStore interface {
	AgentInstallations(context.Context) ([]store.AgentInstallationRecord, error)
	AgentInstances(context.Context) ([]store.AgentInstanceRecord, error)
	WorkspaceAgents(context.Context, string) ([]store.WorkspaceAgentRecord, error)
}

type agentHealthSource interface {
	AgentHealth(string) (adapter.HealthStatus, bool)
}

// ControlSessionOptions binds one authenticated transport to the Node's
// protocol validator, durable event log, workspace policy, and Agent runtime.
// The same component is used by Relay and Standalone compositions.
type ControlSessionOptions struct {
	Transport    transport.Transport
	Validator    *protocol.Validator
	ValidatorV11 *protocolv11.Validator
	Target       protocol.Target
	Events       *eventlog.Manager
	EventsV11    *eventlog.Manager
	Store        controlWorkspaceStore
	Runtime      adapter.Runtime
	DeviceName   string
	RefreshTrust func(context.Context) error
	// EventFailure is called once when the persistent event pump can no longer
	// append Runtime.Events to the local event log. It is intentionally a
	// callback so the host can make the remote control state unavailable
	// without coupling this protocol boundary to host lifecycle code.
	EventFailure func(error)
	Config       configController
	ConfigReload func()
	Now          func() time.Time
}

// ControlSession is the formal Node-side Protocol v1 control boundary.
type ControlSession struct {
	validator    *protocol.Validator
	validatorV11 *protocolv11.Validator
	target       protocol.Target
	events       *eventlog.Manager
	eventsV11    *eventlog.Manager
	store        controlWorkspaceStore
	runtime      adapter.Runtime
	deviceName   string
	refreshTrust func(context.Context) error
	eventFailure func(error)
	config       configController
	configReload func()
	now          func() time.Time
	settingsMu   sync.RWMutex

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

// Reconfigure updates connection-scoped collaborators while preserving the
// Runtime, event log, validator, and persistent event pump.
func (s *ControlSession) Reconfigure(deviceName string, refreshTrust func(context.Context) error, configuration configController) {
	s.settingsMu.Lock()
	s.deviceName = deviceName
	s.refreshTrust = refreshTrust
	s.config = configuration
	s.settingsMu.Unlock()
}

func (s *ControlSession) settings() (string, func(context.Context) error, configController) {
	s.settingsMu.RLock()
	defer s.settingsMu.RUnlock()
	return s.deviceName, s.refreshTrust, s.config
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
		validatorV11: options.ValidatorV11, events: options.Events, eventsV11: options.EventsV11,
		store: options.Store, runtime: options.Runtime, deviceName: options.DeviceName, refreshTrust: options.RefreshTrust,
		active: func() *sessionTransport {
			if options.Transport == nil {
				return nil
			}
			return &sessionTransport{value: options.Transport}
		}(),
		eventFailure: options.EventFailure, config: options.Config, configReload: options.ConfigReload,
		now: func() time.Time {
			if options.Now != nil {
				return options.Now()
			}
			return time.Now()
		},
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
	var header struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if json.Unmarshal(raw, &header) == nil && header.ProtocolVersion == protocolv11.CurrentVersion {
		return s.handleV11(ctx, raw)
	}
	validated, err := s.validator.Validate(ctx, raw, s.target)
	_, refreshTrust, _ := s.settings()
	var validation *protocol.ValidationError
	if err != nil && refreshTrust != nil && errors.As(err, &validation) && validation.Stage == protocol.ValidationStageTrust && validation.Code == protocol.ErrorUnauthorized {
		if refreshErr := refreshTrust(ctx); refreshErr == nil {
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
	var extra map[string]any
	var reload bool
	var dispatchErr error
	if protocol.ControlType(message.Type) == protocol.ControlConfigRead || protocol.ControlType(message.Type) == protocol.ControlConfigUpdate {
		extra, reload, dispatchErr = s.dispatchConfig(ctx, message)
	} else {
		dispatchErr = s.dispatch(ctx, message)
	}
	if dispatchErr != nil {
		status, code = classifyControlFailure(dispatchErr)
	}
	result, err := s.events.CompleteControlWithPayload(ctx, message.MessageID, status, code, "", extra)
	if err != nil {
		return errors.New("node control result persistence failed")
	}
	if err := s.send(ctx, result.Frame); err != nil {
		return err
	}
	if reload && s.configReload != nil {
		go s.configReload()
	}
	return nil
}

func (s *ControlSession) handleV11(ctx context.Context, raw []byte) error {
	if s.validatorV11 == nil || s.eventsV11 == nil {
		return adapter.ErrUnsupported
	}
	target := protocolv11.Target{OwnerID: s.target.OwnerID, NodeID: s.target.NodeID}
	validated, err := s.validatorV11.Validate(ctx, raw, target)
	if err != nil {
		_, refreshTrust, _ := s.settings()
		if refreshTrust != nil && refreshTrust(ctx) == nil {
			validated, err = s.validatorV11.Validate(ctx, raw, target)
		}
	}
	if err != nil {
		return err
	}
	message := validated.Message()
	if _, err := s.eventsV11.BeginControlV11(ctx, validated); err != nil {
		return errors.New("node Protocol 1.1 control persistence failed")
	}
	if _, err := s.eventsV11.MarkDispatching(ctx, message.MessageID); err != nil {
		return errors.New("node Protocol 1.1 control persistence failed")
	}
	if protocolv11.ControlType(message.Type) == protocolv11.ControlEventsReplay {
		return s.handleReplayV11(ctx, message)
	}
	status, code := protocol.ControlResultConfirmed, protocol.ErrorCode("")
	var dispatchErr error
	var extra map[string]any
	var reload bool
	if protocolv11.ControlType(message.Type) == protocolv11.ControlConfigRead || protocolv11.ControlType(message.Type) == protocolv11.ControlConfigUpdate {
		legacy := legacyControlFromV11(message)
		extra, reload, dispatchErr = s.dispatchConfig(ctx, legacy)
	} else {
		dispatchErr = s.dispatchV11(ctx, message)
	}
	if dispatchErr != nil {
		status, code = classifyControlFailure(dispatchErr)
	}
	result, err := s.eventsV11.CompleteControlWithPayload(ctx, message.MessageID, status, code, "", extra)
	if err != nil {
		return errors.New("node Protocol 1.1 control result persistence failed")
	}
	if err := s.send(ctx, result.Frame); err != nil {
		return err
	}
	if reload && s.configReload != nil {
		go s.configReload()
	}
	return nil
}

func (s *ControlSession) dispatchConfig(ctx context.Context, message protocol.YuanshuMessage) (map[string]any, bool, error) {
	_, _, configuration := s.settings()
	if configuration == nil {
		return nil, false, adapter.ErrUnsupported
	}
	switch protocol.ControlType(message.Type) {
	case protocol.ControlConfigRead:
		value, err := configuration.Read(ctx)
		return map[string]any{"config": value}, false, err
	case protocol.ControlConfigUpdate:
		baseRevision := stringPayload(message.Payload, "baseRevision")
		changes := mapPayload(message.Payload, "changes")
		result, err := configuration.Update(ctx, baseRevision, changes)
		return result.Payload, result.Reload, err
	default:
		return nil, false, adapter.ErrUnsupported
	}
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

func (s *ControlSession) handleReplayV11(ctx context.Context, message protocolv11.YuanshuMessage) error {
	s.deliveryMu.Lock()
	defer s.deliveryMu.Unlock()
	batch, replayErr := s.eventsV11.Replay(ctx, int64Payload(message.Payload, "afterSequence"), eventlog.DefaultReplayLimit)
	status, code := protocol.ControlResultConfirmed, protocol.ErrorCode("")
	if replayErr == nil && batch.Gap {
		workspaceID, taskID := value(message.WorkspaceID), value(message.TaskID)
		if workspaceID == "" || taskID == "" {
			replayErr = eventlog.ErrHistoryGap
		} else {
			batch, replayErr = s.eventsV11.Recover(ctx, s.runtime, eventlog.SnapshotTarget{WorkspaceID: workspaceID, ThreadID: taskID}, int64Payload(message.Payload, "afterSequence"), eventlog.DefaultReplayLimit)
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
	result, err := s.eventsV11.CompleteControl(ctx, message.MessageID, status, code, "")
	if err != nil {
		return errors.New("node Protocol 1.1 replay result persistence failed")
	}
	return s.sendFrameLocked(ctx, result.Frame)
}

func (s *ControlSession) dispatch(ctx context.Context, message protocol.YuanshuMessage) error {
	workspaceID, threadID, turnID, itemID := value(message.WorkspaceID), value(message.ThreadID), value(message.TurnID), value(message.ItemID)
	switch protocol.ControlType(message.Type) {
	case protocol.ControlDeviceSync:
		health := s.runtime.Health()
		deviceName, _, _ := s.settings()
		return s.publish(ctx, adapter.AgentEvent{Type: protocol.EventDeviceStatus, CorrelationID: message.MessageID, Payload: map[string]any{
			"status": "online", "name": deviceName, "runtime": health.State, "version": health.CodexVersion, "protocol": health.Protocol,
		}})
	case protocol.ControlWorkspaceList:
		items, err := s.store.Workspaces(ctx)
		if err != nil {
			return adapter.ErrUnavailable
		}
		workspaces := make([]any, 0, len(items))
		for _, item := range items {
			workspaces = append(workspaces, map[string]any{
				"id":                item.ID,
				"name":              item.DisplayName,
				"adapter":           item.Adapter,
				"permissionProfile": item.PermissionProfile,
				"allowNetwork":      item.AllowNetwork,
			})
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
			threads = append(threads, threadPayload(thread))
		}
		return s.publish(ctx, adapter.AgentEvent{Type: protocol.EventThreadSnapshot, CorrelationID: message.MessageID, WorkspaceID: workspaceID, Payload: map[string]any{"status": "listed", "threads": threads, "nextCursor": page.NextCursor}})
	case protocol.ControlThreadRead:
		if workspaceID == "" || threadID == "" {
			return adapter.ErrInvalid
		}
		includeDiffs := true
		if _, exists := message.Payload["includeDiffs"]; exists {
			includeDiffs = boolPayload(message.Payload, "includeDiffs")
		}
		snapshot, err := s.runtime.ReadThread(ctx, adapter.ReadThreadRequest{WorkspaceID: workspaceID, ThreadID: threadID, IncludeTurns: boolPayload(message.Payload, "includeTurns"), IncludeDiffs: includeDiffs, DiffPath: stringPayload(message.Payload, "diffPath"), MaxDiffBytes: intPayload(message.Payload, "maxDiffBytes")})
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
		decision := stringPayload(message.Payload, "decision")
		claimed, err := s.store.ClaimApproval(ctx, store.ApprovalClaim{
			ApprovalID: approvalID, WorkspaceID: workspaceID, ThreadID: threadID, TurnID: turnID, ItemID: itemID,
			OperationDigest: stringPayload(message.Payload, "operationDigest"), Decision: decision, Now: s.now().UTC(),
		})
		if err != nil {
			return err
		}
		err = s.runtime.ResolveApproval(ctx, adapter.ApprovalDecision{WorkspaceID: workspaceID, ThreadID: threadID, TurnID: turnID, ItemID: itemID, ApprovalID: approvalID, Decision: decision})
		if err != nil {
			_ = s.store.MarkApprovalAmbiguous(ctx, claimed.ApprovalID)
		}
		return err
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

func (s *ControlSession) dispatchV11(ctx context.Context, message protocolv11.YuanshuMessage) error {
	agentInstanceID, workspaceID, taskID, runID, interactionID := value(message.AgentInstanceID), value(message.WorkspaceID), value(message.TaskID), value(message.RunID), value(message.InteractionID)
	switch protocolv11.ControlType(message.Type) {
	case protocolv11.ControlAgentList, protocolv11.ControlAgentRead:
		return s.publishAgentSnapshotV11(ctx, message.MessageID, agentInstanceID)
	case protocolv11.ControlTaskList:
		page, err := s.runtime.ListThreads(ctx, adapter.ListThreadsRequest{WorkspaceID: workspaceID, AgentInstanceID: agentInstanceID, Cursor: stringPayload(message.Payload, "cursor"), Limit: intPayload(message.Payload, "limit")})
		if err != nil {
			return err
		}
		tasks := make([]any, 0, len(page.Data))
		for _, task := range page.Data {
			tasks = append(tasks, taskPayloadV11(task, agentInstanceID))
		}
		return s.publishV11Only(ctx, adapter.AgentEvent{Type: protocol.EventType(protocolv11.EventTaskSnapshot), AgentInstanceID: agentInstanceID, CorrelationID: message.MessageID, WorkspaceID: workspaceID, Payload: map[string]any{"tasks": tasks, "nextCursor": page.NextCursor}})
	case protocolv11.ControlTaskRead:
		snapshot, err := s.runtime.ReadThread(ctx, adapter.ReadThreadRequest{WorkspaceID: workspaceID, AgentInstanceID: agentInstanceID, ThreadID: taskID, IncludeTurns: boolPayload(message.Payload, "includeRuns"), IncludeDiffs: boolPayload(message.Payload, "includeDiffs"), DiffPath: stringPayload(message.Payload, "diffPath"), MaxDiffBytes: intPayload(message.Payload, "maxDiffBytes")})
		if err != nil {
			return err
		}
		return s.publishSnapshot(ctx, message.MessageID, snapshot)
	case protocolv11.ControlTaskStart:
		task, err := s.runtime.StartThread(ctx, adapter.StartThreadRequest{WorkspaceID: workspaceID, AgentInstanceID: agentInstanceID})
		if err != nil {
			return err
		}
		if err := s.publish(ctx, adapter.AgentEvent{Type: protocol.EventThreadStarted, AgentInstanceID: agentInstanceID, CorrelationID: message.MessageID, WorkspaceID: workspaceID, ThreadID: task.ID, Payload: map[string]any{"status": task.Status}}); err != nil {
			return err
		}
		run, err := s.runtime.StartTurn(ctx, adapter.StartTurnRequest{WorkspaceID: workspaceID, ThreadID: task.ID, Input: stringPayload(message.Payload, "input")})
		if err != nil {
			return err
		}
		return s.publish(ctx, adapter.AgentEvent{Type: protocol.EventTurnStarted, AgentInstanceID: agentInstanceID, CorrelationID: message.MessageID, WorkspaceID: workspaceID, ThreadID: task.ID, TurnID: run.ID, Payload: map[string]any{"status": run.Status}})
	case protocolv11.ControlTaskResume:
		task, err := s.runtime.ResumeThread(ctx, adapter.ResumeThreadRequest{WorkspaceID: workspaceID, AgentInstanceID: agentInstanceID, ThreadID: taskID})
		if err != nil {
			return err
		}
		return s.publish(ctx, adapter.AgentEvent{Type: protocol.EventThreadStarted, AgentInstanceID: agentInstanceID, CorrelationID: message.MessageID, WorkspaceID: workspaceID, ThreadID: task.ID, Payload: map[string]any{"status": "resumed"}})
	case protocolv11.ControlRunStart:
		run, err := s.runtime.StartTurn(ctx, adapter.StartTurnRequest{WorkspaceID: workspaceID, ThreadID: taskID, Input: stringPayload(message.Payload, "input")})
		if err != nil {
			return err
		}
		return s.publish(ctx, adapter.AgentEvent{Type: protocol.EventTurnStarted, AgentInstanceID: agentInstanceID, CorrelationID: message.MessageID, WorkspaceID: workspaceID, ThreadID: taskID, TurnID: run.ID, Payload: map[string]any{"status": run.Status}})
	case protocolv11.ControlRunSteer:
		return s.runtime.SteerTurn(ctx, adapter.SteerTurnRequest{WorkspaceID: workspaceID, ThreadID: taskID, TurnID: runID, Input: stringPayload(message.Payload, "input")})
	case protocolv11.ControlRunInterrupt:
		return s.runtime.InterruptTurn(ctx, adapter.InterruptTurnRequest{WorkspaceID: workspaceID, ThreadID: taskID, TurnID: runID})
	case protocolv11.ControlInteractionResolve:
		decision := stringPayload(message.Payload, "decision")
		if decision == "" {
			return adapter.ErrUnsupported
		}
		approval, err := s.store.Approval(ctx, interactionID)
		if err != nil || approval.WorkspaceID != workspaceID || approval.ThreadID != taskID || approval.TurnID != runID {
			if err != nil {
				return err
			}
			return adapter.ErrForbidden
		}
		claimed, err := s.store.ClaimApproval(ctx, store.ApprovalClaim{ApprovalID: interactionID, WorkspaceID: workspaceID, ThreadID: taskID, TurnID: runID, ItemID: approval.ItemID, OperationDigest: stringPayload(message.Payload, "operationDigest"), Decision: decision, Now: s.now().UTC()})
		if err != nil {
			return err
		}
		err = s.runtime.ResolveApproval(ctx, adapter.ApprovalDecision{WorkspaceID: workspaceID, ThreadID: taskID, TurnID: runID, ItemID: approval.ItemID, ApprovalID: interactionID, Decision: decision})
		if err != nil {
			_ = s.store.MarkApprovalAmbiguous(ctx, claimed.ApprovalID)
		}
		return err
	case protocolv11.ControlDeviceSync:
		return s.dispatch(ctx, legacyControlFromV11(message))
	case protocolv11.ControlWorkspaceList:
		items, err := s.store.Workspaces(ctx)
		if err != nil {
			return adapter.ErrUnavailable
		}
		resources, _ := s.store.(controlAgentStore)
		workspaces := make([]any, 0, len(items))
		for _, item := range items {
			entry := map[string]any{"id": item.ID, "name": item.DisplayName, "permissionProfile": item.PermissionProfile, "allowNetwork": item.AllowNetwork}
			if resources != nil {
				links, linkErr := resources.WorkspaceAgents(ctx, item.ID)
				if linkErr != nil {
					return adapter.ErrUnavailable
				}
				agents := make([]any, 0, len(links))
				for _, link := range links {
					agents = append(agents, map[string]any{"agentInstanceId": link.InstanceID, "default": link.Default})
				}
				entry["agents"] = agents
			}
			workspaces = append(workspaces, entry)
		}
		return s.publishV11Only(ctx, adapter.AgentEvent{Type: protocol.EventType(protocolv11.EventDeviceStatus), CorrelationID: message.MessageID, Payload: map[string]any{"status": "online", "workspaces": workspaces}})
	case protocolv11.ControlSnapshotRequest:
		record, err := s.eventsV11.Snapshot(ctx, s.runtime, eventlog.SnapshotTarget{WorkspaceID: workspaceID, ThreadID: taskID})
		if err != nil {
			return err
		}
		return s.send(ctx, record.Frame)
	default:
		return adapter.ErrUnsupported
	}
}

func (s *ControlSession) publishAgentSnapshotV11(ctx context.Context, correlationID, requestedID string) error {
	resources, ok := s.store.(controlAgentStore)
	if !ok {
		return adapter.ErrUnsupported
	}
	instances, err := resources.AgentInstances(ctx)
	if err != nil {
		return adapter.ErrUnavailable
	}
	installations, _ := resources.AgentInstallations(ctx)
	installationByType := make(map[string]store.AgentInstallationRecord, len(installations))
	for _, item := range installations {
		installationByType[item.AdapterType] = item
	}
	healthByAgent, _ := s.runtime.(agentHealthSource)
	agents := make([]any, 0, len(instances))
	for _, instance := range instances {
		if requestedID != "" && instance.InstanceID != requestedID {
			continue
		}
		installation := installationByType[instance.AdapterType]
		status := installation.InstallationState
		if status == "" {
			status = "unknown"
		}
		controllable := false
		if healthByAgent != nil {
			if health, exists := healthByAgent.AgentHealth(instance.InstanceID); exists {
				status, controllable = health.State, health.State == "ready"
			}
		}
		capabilities := []any{
			capabilityPayload("task.read", instance.RuntimeMode == store.AgentRuntimeManaged, "runtime_not_managed"),
			capabilityPayload("task.start", controllable, "runtime_not_ready"),
			capabilityPayload("run.control", controllable, "runtime_not_ready"),
		}
		agents = append(agents, map[string]any{
			"id": instance.InstanceID, "adapterType": instance.AdapterType, "displayName": instance.DisplayName,
			"version": installation.Version, "runtimeMode": instance.RuntimeMode, "status": status,
			"authenticationAvailable": controllable, "capabilities": capabilities,
		})
	}
	if requestedID != "" && len(agents) == 0 {
		return adapter.ErrNotFound
	}
	return s.publishV11Only(ctx, adapter.AgentEvent{Type: protocol.EventType(protocolv11.EventAgentSnapshot), CorrelationID: correlationID, AgentInstanceID: requestedID, Payload: map[string]any{"agents": agents}})
}

func capabilityPayload(id string, available bool, reason string) map[string]any {
	level := "unavailable"
	if available {
		level, reason = "full", ""
	}
	result := map[string]any{"id": id, "level": level}
	if reason != "" {
		result["reason"] = reason
	}
	return result
}

func taskPayloadV11(task adapter.Thread, fallbackAgentID string) map[string]any {
	agentID := task.AgentInstanceID
	if agentID == "" {
		agentID = fallbackAgentID
	}
	payload := threadPayload(task)
	payload["agentInstanceId"], payload["workspaceId"] = agentID, task.WorkspaceID
	return payload
}

func legacyControlFromV11(message protocolv11.YuanshuMessage) protocol.YuanshuMessage {
	legacyType := map[protocolv11.ControlType]protocol.ControlType{
		protocolv11.ControlDeviceSync: protocol.ControlDeviceSync, protocolv11.ControlWorkspaceList: protocol.ControlWorkspaceList,
		protocolv11.ControlConfigRead: protocol.ControlConfigRead, protocolv11.ControlConfigUpdate: protocol.ControlConfigUpdate,
	}[protocolv11.ControlType(message.Type)]
	return protocol.YuanshuMessage{ProtocolVersion: protocol.CurrentVersion, MessageID: message.MessageID, Type: string(legacyType), OwnerID: message.OwnerID, NodeID: message.NodeID, WorkspaceID: message.WorkspaceID, ThreadID: message.TaskID, TurnID: message.RunID, ItemID: message.InteractionID, CorrelationID: message.CorrelationID, Payload: message.Payload}
}

func (s *ControlSession) publishSnapshot(ctx context.Context, correlationID string, snapshot adapter.ThreadSnapshot) error {
	turns := make([]any, 0, len(snapshot.Turns))
	for _, turn := range snapshot.Turns {
		items := make([]any, 0, len(turn.Items))
		for _, item := range turn.Items {
			items = append(items, threadItemPayload(item))
		}
		turns = append(turns, map[string]any{"id": turn.ID, "status": turn.Status, "historyState": turn.HistoryState, "items": items})
	}
	payload := threadPayload(snapshot.Thread)
	payload["historyState"] = snapshot.Thread.HistoryState
	payload["turns"] = turns
	return s.publish(ctx, adapter.AgentEvent{Type: protocol.EventThreadSnapshot, AgentInstanceID: snapshot.Thread.AgentInstanceID, CorrelationID: correlationID, WorkspaceID: snapshot.Thread.WorkspaceID, ThreadID: snapshot.Thread.ID, Payload: payload})
}

func threadPayload(thread adapter.Thread) map[string]any {
	payload := map[string]any{"id": thread.ID, "status": thread.Status}
	if thread.Title != "" {
		payload["title"] = thread.Title
	}
	if thread.Preview != "" {
		payload["preview"] = thread.Preview
	}
	if thread.HistoryState != "" {
		payload["historyState"] = thread.HistoryState
	}
	if !thread.CreatedAt.IsZero() {
		payload["createdAt"] = thread.CreatedAt.Format(time.RFC3339Nano)
	}
	if !thread.UpdatedAt.IsZero() {
		payload["updatedAt"] = thread.UpdatedAt.Format(time.RFC3339Nano)
	}
	return payload
}

func threadItemPayload(item adapter.ThreadItem) map[string]any {
	payload := map[string]any{"id": item.ID, "kind": item.Kind, "status": item.Status}
	if item.Text != "" {
		payload["text"] = item.Text
	}
	if item.Command != "" {
		payload["command"] = item.Command
	}
	if item.Output != "" {
		payload["output"] = item.Output
	}
	if item.ToolName != "" {
		payload["toolName"] = item.ToolName
	}
	if item.Path != "" {
		payload["path"] = item.Path
	}
	if item.ChangeType != "" {
		payload["changeType"] = item.ChangeType
	}
	if item.Diff != "" {
		payload["diff"] = item.Diff
	}
	if item.ExitCode != nil {
		payload["exitCode"] = *item.ExitCode
	}
	if item.ErrorCode != "" {
		payload["errorCode"] = item.ErrorCode
	}
	if item.ErrorMessage != "" {
		payload["errorMessage"] = item.ErrorMessage
	}
	if item.Partial {
		payload["partial"] = true
	}
	if item.Truncated {
		payload["truncated"] = true
	}
	if item.DiffTotalBytes > 0 {
		payload["totalBytes"] = item.DiffTotalBytes
	}
	if item.DiffDigest != "" {
		payload["digest"] = item.DiffDigest
	}
	return payload
}

func (s *ControlSession) publish(ctx context.Context, event adapter.AgentEvent) error {
	records, err := s.events.Publish(ctx, event)
	if err != nil {
		return errors.New("node event persistence failed")
	}
	var recordsV11 []eventlog.Record
	if s.eventsV11 != nil {
		recordsV11, err = s.eventsV11.Publish(ctx, event)
		if err != nil {
			return errors.New("node Protocol 1.1 event persistence failed")
		}
	}
	for _, record := range records {
		_ = s.sendFrame(ctx, record.Frame)
	}
	for _, record := range recordsV11 {
		_ = s.sendFrame(ctx, record.Frame)
	}
	return nil
}

func (s *ControlSession) publishV11Only(ctx context.Context, event adapter.AgentEvent) error {
	if s.eventsV11 == nil {
		return adapter.ErrUnsupported
	}
	records, err := s.eventsV11.Publish(ctx, event)
	if err != nil {
		return errors.New("node Protocol 1.1 event persistence failed")
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
	case errors.Is(err, adapter.ErrConflict), errors.Is(err, store.ErrConflict):
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

func mapPayload(payload map[string]any, key string) map[string]any {
	value, _ := payload[key].(map[string]any)
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
