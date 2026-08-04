// Package runtime owns the Node-local lifecycle and isolation of Agent
// Runtime connections. It deliberately has no Server or Protocol dependency.
package runtime

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/yuanshu-ai/yuanshu/internal/adapter"
)

type RuntimeKey struct {
	InstanceID string
	EndpointID string
}

type Factory func(context.Context) (adapter.Runtime, error)

type OpenRequest struct {
	Key     RuntimeKey
	Mode    adapter.RuntimeMode
	Factory Factory
}

type Options struct {
	EventCapacity int
	CloseTimeout  time.Duration
}

type Manager struct {
	options Options
	mu      sync.RWMutex
	entries map[RuntimeKey]*entry
	order   []RuntimeKey
	closed  bool
}

type Handle struct{ entry *entry }

type entry struct {
	key     RuntimeKey
	mode    adapter.RuntimeMode
	runtime adapter.Runtime
	events  chan adapter.AgentEvent
	stop    chan struct{}
	done    chan struct{}

	mu          sync.RWMutex
	health      adapter.HealthStatus
	closing     bool
	closed      bool
	closeOnce   sync.Once
	closeErr    error
	closeTimout time.Duration
}

var _ adapter.Runtime = Handle{}

func NewManager(options Options) (*Manager, error) {
	if options.EventCapacity < 0 || options.CloseTimeout < 0 {
		return nil, adapter.ErrInvalid
	}
	if options.EventCapacity == 0 {
		options.EventCapacity = 256
	}
	if options.CloseTimeout == 0 {
		options.CloseTimeout = 10 * time.Second
	}
	return &Manager{options: options, entries: make(map[RuntimeKey]*entry)}, nil
}

func (m *Manager) Open(ctx context.Context, request OpenRequest) (Handle, error) {
	if m == nil || ctx == nil || !validKey(request.Key) || request.Factory == nil {
		if ctx == nil {
			return Handle{}, context.Canceled
		}
		return Handle{}, adapter.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return Handle{}, err
	}
	if request.Mode != adapter.RuntimeManaged {
		return Handle{}, adapter.ErrUnsupported
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return Handle{}, adapter.ErrClosed
	}
	if existing := m.entries[request.Key]; existing != nil && !existing.isClosed() {
		m.mu.Unlock()
		return Handle{}, adapter.ErrConflict
	}
	// Reserve the key so concurrent Factory calls cannot create two runtimes.
	reserved := &entry{key: request.Key}
	m.entries[request.Key] = reserved
	m.mu.Unlock()

	created, err := request.Factory(ctx)
	if err != nil || created == nil {
		m.mu.Lock()
		if m.entries[request.Key] == reserved {
			delete(m.entries, request.Key)
		}
		m.mu.Unlock()
		if err == nil {
			return Handle{}, adapter.ErrInvalid
		}
		return Handle{}, normalizeError(err)
	}
	value := &entry{
		key: request.Key, mode: request.Mode, runtime: created,
		events: make(chan adapter.AgentEvent, m.options.EventCapacity),
		stop:   make(chan struct{}), done: make(chan struct{}),
		health: created.Health(), closeTimout: m.options.CloseTimeout,
	}
	m.mu.Lock()
	if m.closed || m.entries[request.Key] != reserved {
		m.mu.Unlock()
		closeCtx, cancel := context.WithTimeout(context.Background(), m.options.CloseTimeout)
		_ = created.Close(closeCtx)
		cancel()
		return Handle{}, adapter.ErrClosed
	}
	m.entries[request.Key] = value
	m.order = append(m.order, request.Key)
	m.mu.Unlock()
	go value.pump()
	return Handle{entry: value}, nil
}

func (m *Manager) Get(key RuntimeKey) (Handle, error) {
	if m == nil || !validKey(key) {
		return Handle{}, adapter.ErrInvalid
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.closed {
		return Handle{}, adapter.ErrClosed
	}
	value := m.entries[key]
	if value == nil {
		return Handle{}, adapter.ErrNotFound
	}
	if value.runtime == nil {
		return Handle{}, adapter.ErrConflict
	}
	if value.isClosed() {
		return Handle{}, adapter.ErrClosed
	}
	return Handle{entry: value}, nil
}

func (m *Manager) Close(ctx context.Context) error {
	if m == nil {
		return nil
	}
	if ctx == nil {
		return context.Canceled
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	entries := make([]*entry, 0, len(m.order))
	seen := make(map[*entry]struct{}, len(m.order))
	for index := len(m.order) - 1; index >= 0; index-- {
		if value := m.entries[m.order[index]]; value != nil && value.runtime != nil {
			if _, ok := seen[value]; !ok {
				seen[value] = struct{}{}
				entries = append(entries, value)
			}
		}
	}
	m.mu.Unlock()
	var result error
	for _, value := range entries {
		if err := value.close(ctx, true); err != nil {
			result = errors.Join(result, err)
		}
	}
	return result
}

func (e *entry) pump() {
	defer close(e.done)
	defer close(e.events)
	for {
		select {
		case <-e.stop:
			e.markClosed("")
			return
		case event, ok := <-e.runtime.Events():
			if !ok {
				e.mu.RLock()
				closing := e.closing
				e.mu.RUnlock()
				if closing {
					e.markClosed("")
				} else {
					e.markClosed("runtime_exited")
				}
				return
			}
			select {
			case e.events <- event:
			case <-e.stop:
				e.markClosed("")
				return
			default:
				e.markFailed("event_backpressure")
				ctx, cancel := context.WithTimeout(context.Background(), e.closeTimout)
				e.closeOnce.Do(func() { e.closeErr = e.runtime.Close(ctx) })
				cancel()
				return
			}
		}
	}
}

func (e *entry) close(ctx context.Context, wait bool) error {
	if e == nil || e.runtime == nil {
		return adapter.ErrInvalid
	}
	e.mu.Lock()
	e.closing = true
	e.mu.Unlock()
	e.closeOnce.Do(func() {
		close(e.stop)
		e.closeErr = e.runtime.Close(ctx)
	})
	if wait {
		select {
		case <-e.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return normalizeCloseError(e.closeErr)
}

func (e *entry) isClosed() bool {
	if e == nil {
		return true
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.closed
}

func (e *entry) markFailed(code string) {
	e.mu.Lock()
	e.health.State, e.health.FailureCode = "failed", code
	e.closed = true
	e.mu.Unlock()
}

func (e *entry) markClosed(failure string) {
	e.mu.Lock()
	e.closed = true
	if failure != "" {
		e.health.State, e.health.FailureCode = "failed", failure
	} else if e.health.State != "failed" {
		e.health.State = "closed"
	}
	e.mu.Unlock()
}

func (h Handle) RuntimeKey() RuntimeKey {
	if h.entry == nil {
		return RuntimeKey{}
	}
	return h.entry.key
}

func (h Handle) Events() <-chan adapter.AgentEvent {
	if h.entry == nil {
		closed := make(chan adapter.AgentEvent)
		close(closed)
		return closed
	}
	return h.entry.events
}

func (h Handle) Health() adapter.HealthStatus {
	if h.entry == nil {
		return adapter.HealthStatus{State: "failed", FailureCode: "invalid_runtime"}
	}
	h.entry.mu.RLock()
	health := h.entry.health
	h.entry.mu.RUnlock()
	if !h.entry.isClosed() {
		underlying := h.entry.runtime.Health()
		if underlying.State != "" {
			health = underlying
		}
	}
	return health
}

func (h Handle) Close(ctx context.Context) error { return h.entry.close(ctx, true) }

func (h Handle) ListThreads(ctx context.Context, request adapter.ListThreadsRequest) (adapter.ThreadPage, error) {
	if runtime, err := h.usable(); err != nil {
		return adapter.ThreadPage{}, err
	} else {
		return runtime.ListThreads(ctx, request)
	}
}
func (h Handle) ReadThread(ctx context.Context, request adapter.ReadThreadRequest) (adapter.ThreadSnapshot, error) {
	if runtime, err := h.usable(); err != nil {
		return adapter.ThreadSnapshot{}, err
	} else {
		return runtime.ReadThread(ctx, request)
	}
}
func (h Handle) StartThread(ctx context.Context, request adapter.StartThreadRequest) (adapter.Thread, error) {
	if runtime, err := h.usable(); err != nil {
		return adapter.Thread{}, err
	} else {
		return runtime.StartThread(ctx, request)
	}
}
func (h Handle) ResumeThread(ctx context.Context, request adapter.ResumeThreadRequest) (adapter.Thread, error) {
	if runtime, err := h.usable(); err != nil {
		return adapter.Thread{}, err
	} else {
		return runtime.ResumeThread(ctx, request)
	}
}
func (h Handle) StartTurn(ctx context.Context, request adapter.StartTurnRequest) (adapter.Turn, error) {
	if runtime, err := h.usable(); err != nil {
		return adapter.Turn{}, err
	} else {
		return runtime.StartTurn(ctx, request)
	}
}
func (h Handle) SteerTurn(ctx context.Context, request adapter.SteerTurnRequest) error {
	if runtime, err := h.usable(); err != nil {
		return err
	} else {
		return runtime.SteerTurn(ctx, request)
	}
}
func (h Handle) InterruptTurn(ctx context.Context, request adapter.InterruptTurnRequest) error {
	if runtime, err := h.usable(); err != nil {
		return err
	} else {
		return runtime.InterruptTurn(ctx, request)
	}
}
func (h Handle) ResolveApproval(ctx context.Context, request adapter.ApprovalDecision) error {
	if runtime, err := h.usable(); err != nil {
		return err
	} else {
		return runtime.ResolveApproval(ctx, request)
	}
}

func (h Handle) usable() (adapter.Runtime, error) {
	if h.entry == nil || h.entry.runtime == nil {
		return nil, adapter.ErrInvalid
	}
	if h.entry.isClosed() {
		return nil, adapter.ErrClosed
	}
	return h.entry.runtime, nil
}

func validKey(key RuntimeKey) bool {
	return validIdentifier(key.InstanceID) && validIdentifier(key.EndpointID)
}

func validIdentifier(value string) bool {
	if len(value) < 1 || len(value) > 64 || strings.ToLower(value) != value {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || strings.ContainsRune("._-", character) {
			continue
		}
		return false
	}
	return true
}

func normalizeError(err error) error {
	for _, candidate := range []error{adapter.ErrInvalid, adapter.ErrUnavailable, adapter.ErrUnsupported, adapter.ErrNotFound, adapter.ErrConflict, adapter.ErrForbidden, adapter.ErrReconciliationNeeded, adapter.ErrAmbiguous, adapter.ErrClosed} {
		if errors.Is(err, candidate) {
			return candidate
		}
	}
	return adapter.ErrUnavailable
}

func normalizeCloseError(err error) error {
	if err == nil || errors.Is(err, adapter.ErrClosed) {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return adapter.ErrUnavailable
}
