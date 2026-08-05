package runtime

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/yuanshu-ai/yuanshu/internal/adapter"
	"github.com/yuanshu-ai/yuanshu/internal/node/store"
)

type BindingStore interface {
	SaveTaskBinding(context.Context, store.TaskBindingRecord) error
	TaskBinding(context.Context, string) (store.TaskBindingRecord, error)
	TaskBindingByNativeSession(context.Context, string, string) (store.TaskBindingRecord, error)
	WorkspaceAgents(context.Context, string) ([]store.WorkspaceAgentRecord, error)
}

type Source struct {
	Key     RuntimeKey
	Runtime adapter.Runtime
}

type RouterOptions struct {
	Store             BindingStore
	Sources           []Source
	DefaultInstanceID string
	Random            io.Reader
	EventCapacity     int
}

type Router struct {
	store             BindingStore
	sources           map[string]Source
	defaultInstanceID string
	random            io.Reader
	events            chan adapter.AgentEvent
	stop              chan struct{}
	done              chan struct{}
	closeOnce         sync.Once
	workers           sync.WaitGroup
}

var _ adapter.Runtime = (*Router)(nil)

func NewRouter(options RouterOptions) (*Router, error) {
	if options.Store == nil || options.DefaultInstanceID == "" || len(options.Sources) == 0 || options.EventCapacity < 0 {
		return nil, adapter.ErrInvalid
	}
	if options.Random == nil {
		options.Random = rand.Reader
	}
	if options.EventCapacity == 0 {
		options.EventCapacity = 512
	}
	router := &Router{store: options.Store, sources: make(map[string]Source, len(options.Sources)), defaultInstanceID: options.DefaultInstanceID, random: options.Random, events: make(chan adapter.AgentEvent, options.EventCapacity), stop: make(chan struct{}), done: make(chan struct{})}
	for _, source := range options.Sources {
		if source.Key.InstanceID == "" || source.Key.EndpointID == "" || source.Runtime == nil {
			return nil, adapter.ErrInvalid
		}
		if _, exists := router.sources[source.Key.InstanceID]; exists {
			return nil, adapter.ErrConflict
		}
		router.sources[source.Key.InstanceID] = source
	}
	if _, exists := router.sources[options.DefaultInstanceID]; !exists {
		return nil, adapter.ErrNotFound
	}
	for _, source := range router.sources {
		router.workers.Add(1)
		go router.pump(source)
	}
	go func() { router.workers.Wait(); close(router.done); close(router.events) }()
	return router, nil
}

func (r *Router) Events() <-chan adapter.AgentEvent { return r.events }

func (r *Router) Health() adapter.HealthStatus {
	if r == nil {
		return adapter.HealthStatus{State: "unavailable", FailureCode: "invalid_runtime"}
	}
	return r.sources[r.defaultInstanceID].Runtime.Health()
}

func (r *Router) Close(ctx context.Context) error {
	if r == nil {
		return nil
	}
	if ctx == nil {
		return context.Canceled
	}
	var result error
	r.closeOnce.Do(func() {
		close(r.stop)
		for _, source := range r.sources {
			result = errors.Join(result, source.Runtime.Close(ctx))
		}
	})
	select {
	case <-r.done:
		return result
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *Router) ListThreads(ctx context.Context, request adapter.ListThreadsRequest) (adapter.ThreadPage, error) {
	source, err := r.sourceFor(ctx, request.WorkspaceID, request.AgentInstanceID)
	if err != nil {
		return adapter.ThreadPage{}, err
	}
	request.AgentInstanceID = ""
	page, err := source.Runtime.ListThreads(ctx, request)
	if err != nil {
		return adapter.ThreadPage{}, err
	}
	for index := range page.Data {
		translated, err := r.bindThread(ctx, source, page.Data[index], "resumed")
		if err != nil {
			return adapter.ThreadPage{}, err
		}
		page.Data[index] = translated
	}
	return page, nil
}

func (r *Router) ReadThread(ctx context.Context, request adapter.ReadThreadRequest) (adapter.ThreadSnapshot, error) {
	binding, source, err := r.resolveTask(ctx, request.WorkspaceID, request.ThreadID, request.AgentInstanceID)
	if err != nil {
		return adapter.ThreadSnapshot{}, err
	}
	request.AgentInstanceID, request.ThreadID = "", binding.NativeSessionID
	snapshot, err := source.Runtime.ReadThread(ctx, request)
	if err != nil {
		return adapter.ThreadSnapshot{}, err
	}
	return translateSnapshot(snapshot, binding.TaskID, binding.InstanceID), nil
}

func (r *Router) StartThread(ctx context.Context, request adapter.StartThreadRequest) (adapter.Thread, error) {
	source, err := r.sourceFor(ctx, request.WorkspaceID, request.AgentInstanceID)
	if err != nil {
		return adapter.Thread{}, err
	}
	request.AgentInstanceID = ""
	thread, err := source.Runtime.StartThread(ctx, request)
	if err != nil {
		return adapter.Thread{}, err
	}
	bound, err := r.bindThread(ctx, source, thread, "created")
	if err != nil {
		return adapter.Thread{}, adapter.ErrAmbiguous
	}
	return bound, nil
}

func (r *Router) ResumeThread(ctx context.Context, request adapter.ResumeThreadRequest) (adapter.Thread, error) {
	binding, source, err := r.resolveTask(ctx, request.WorkspaceID, request.ThreadID, request.AgentInstanceID)
	if err != nil {
		return adapter.Thread{}, err
	}
	request.AgentInstanceID, request.ThreadID = "", binding.NativeSessionID
	thread, err := source.Runtime.ResumeThread(ctx, request)
	if err != nil {
		return adapter.Thread{}, err
	}
	thread.ID, thread.AgentInstanceID = binding.TaskID, binding.InstanceID
	return thread, r.saveBindingState(ctx, binding, store.RuntimeThreadIdle, "")
}

func (r *Router) StartTurn(ctx context.Context, request adapter.StartTurnRequest) (adapter.Turn, error) {
	binding, source, err := r.resolveTask(ctx, request.WorkspaceID, request.ThreadID, "")
	if err != nil {
		return adapter.Turn{}, err
	}
	request.ThreadID = binding.NativeSessionID
	turn, err := source.Runtime.StartTurn(ctx, request)
	if err != nil {
		return adapter.Turn{}, err
	}
	turn.ThreadID = binding.TaskID
	if err := r.saveBindingState(ctx, binding, store.RuntimeThreadActive, turn.ID); err != nil {
		return adapter.Turn{}, adapter.ErrAmbiguous
	}
	return turn, nil
}

func (r *Router) SteerTurn(ctx context.Context, request adapter.SteerTurnRequest) error {
	binding, source, err := r.resolveTask(ctx, request.WorkspaceID, request.ThreadID, "")
	if err != nil {
		return err
	}
	request.ThreadID = binding.NativeSessionID
	return source.Runtime.SteerTurn(ctx, request)
}

func (r *Router) InterruptTurn(ctx context.Context, request adapter.InterruptTurnRequest) error {
	binding, source, err := r.resolveTask(ctx, request.WorkspaceID, request.ThreadID, "")
	if err != nil {
		return err
	}
	request.ThreadID = binding.NativeSessionID
	return source.Runtime.InterruptTurn(ctx, request)
}

func (r *Router) ResolveApproval(ctx context.Context, request adapter.ApprovalDecision) error {
	binding, source, err := r.resolveTask(ctx, request.WorkspaceID, request.ThreadID, "")
	if err != nil {
		return err
	}
	request.ThreadID = binding.NativeSessionID
	return source.Runtime.ResolveApproval(ctx, request)
}

func (r *Router) pump(source Source) {
	defer r.workers.Done()
	for {
		select {
		case <-r.stop:
			return
		case event, ok := <-source.Runtime.Events():
			if !ok {
				return
			}
			event.AgentInstanceID = source.Key.InstanceID
			if event.ThreadID != "" {
				binding, err := r.waitForEventBinding(source.Key.InstanceID, event.ThreadID)
				if err != nil {
					select {
					case r.events <- adapter.AgentEvent{Type: "error", AgentInstanceID: source.Key.InstanceID, CorrelationID: event.CorrelationID, WorkspaceID: event.WorkspaceID, Payload: map[string]any{"code": "task_binding_unavailable"}}:
					case <-r.stop:
						return
					}
					continue
				}
				event.ThreadID = binding.TaskID
				if event.Type == "turn.completed" || event.Type == "turn.failed" || event.Type == "turn.interrupted" {
					_ = r.saveBindingState(context.Background(), binding, store.RuntimeThreadIdle, "")
				}
			}
			select {
			case r.events <- event:
			case <-r.stop:
				return
			}
		}
	}
}

func (r *Router) waitForEventBinding(instanceID, nativeSessionID string) (store.TaskBindingRecord, error) {
	for attempt := 0; attempt < 100; attempt++ {
		binding, err := r.store.TaskBindingByNativeSession(context.Background(), instanceID, nativeSessionID)
		if err == nil || !errors.Is(err, store.ErrNotFound) {
			return binding, err
		}
		timer := time.NewTimer(20 * time.Millisecond)
		select {
		case <-timer.C:
		case <-r.stop:
			timer.Stop()
			return store.TaskBindingRecord{}, adapter.ErrClosed
		}
	}
	return store.TaskBindingRecord{}, store.ErrNotFound
}

func (r *Router) sourceFor(ctx context.Context, workspaceID, requested string) (Source, error) {
	instanceID := requested
	links, err := r.store.WorkspaceAgents(ctx, workspaceID)
	if err != nil {
		return Source{}, adapter.ErrUnavailable
	}
	if instanceID == "" {
		for _, link := range links {
			if link.Default {
				instanceID = link.InstanceID
				break
			}
		}
	}
	allowed := false
	for _, link := range links {
		if link.InstanceID == instanceID {
			allowed = true
			break
		}
	}
	if !allowed {
		return Source{}, adapter.ErrForbidden
	}
	source, exists := r.sources[instanceID]
	if !exists {
		return Source{}, adapter.ErrUnavailable
	}
	return source, nil
}

func (r *Router) resolveTask(ctx context.Context, workspaceID, taskID, hintedInstanceID string) (store.TaskBindingRecord, Source, error) {
	binding, err := r.store.TaskBinding(ctx, taskID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) && hintedInstanceID != "" {
			binding, err = r.store.TaskBindingByNativeSession(ctx, hintedInstanceID, taskID)
		}
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return binding, Source{}, adapter.ErrNotFound
			}
			return binding, Source{}, adapter.ErrUnavailable
		}
	}
	if binding.WorkspaceID != workspaceID {
		return binding, Source{}, adapter.ErrForbidden
	}
	source, exists := r.sources[binding.InstanceID]
	if !exists || source.Key.EndpointID != binding.EndpointID {
		return binding, Source{}, adapter.ErrUnavailable
	}
	return binding, source, nil
}

func (r *Router) bindThread(ctx context.Context, source Source, thread adapter.Thread, ownership string) (adapter.Thread, error) {
	if existing, err := r.store.TaskBindingByNativeSession(ctx, source.Key.InstanceID, thread.ID); err == nil {
		thread.ID, thread.AgentInstanceID = existing.TaskID, existing.InstanceID
		return thread, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return adapter.Thread{}, adapter.ErrUnavailable
	}
	taskID, err := r.newTaskID()
	if err != nil {
		return adapter.Thread{}, adapter.ErrUnavailable
	}
	binding := store.TaskBindingRecord{TaskID: taskID, InstanceID: source.Key.InstanceID, EndpointID: source.Key.EndpointID, WorkspaceID: thread.WorkspaceID, NativeSessionID: thread.ID, Ownership: ownership, State: store.RuntimeThreadIdle}
	if err := r.store.SaveTaskBinding(ctx, binding); err != nil {
		return adapter.Thread{}, adapter.ErrUnavailable
	}
	thread.ID, thread.AgentInstanceID = taskID, source.Key.InstanceID
	return thread, nil
}

func (r *Router) newTaskID() (string, error) {
	value := make([]byte, 18)
	if _, err := io.ReadFull(r.random, value); err != nil {
		return "", err
	}
	return "task_" + base64.RawURLEncoding.EncodeToString(value), nil
}

func (r *Router) saveBindingState(ctx context.Context, binding store.TaskBindingRecord, state, runID string) error {
	binding.State, binding.ActiveRunID = state, runID
	return r.store.SaveTaskBinding(ctx, binding)
}

func translateSnapshot(snapshot adapter.ThreadSnapshot, taskID, instanceID string) adapter.ThreadSnapshot {
	snapshot.Thread.ID, snapshot.Thread.AgentInstanceID = taskID, instanceID
	for index := range snapshot.Turns {
		snapshot.Turns[index].ThreadID = taskID
	}
	return snapshot
}
