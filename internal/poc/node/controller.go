// Package node owns the temporary M0 PoC task, approval, and replay state.
// All Agent Runtime access remains behind this package boundary.
package node

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/yuanshu-ai/yuanshu/internal/poc/protocol"
	"github.com/yuanshu-ai/yuanshu/internal/poc/transport"
)

const (
	MaxEvents              = 256
	MaxEventBytes          = 1 << 20
	DefaultApprovalTimeout = 5 * time.Minute
)

var (
	ErrActiveTurn      = errors.New("a PoC turn is already active")
	ErrUnknownApproval = errors.New("unknown or already resolved approval")
)

type RuntimeEvent struct {
	Type      string
	Payload   any
	Approval  *RuntimeApproval
	Terminal  bool
	Ambiguous bool
}

type RuntimeApproval struct {
	Handle  string
	Kind    string
	Summary string
}

type Runtime interface {
	Start(context.Context, string) (<-chan RuntimeEvent, error)
	Resolve(context.Context, string, string) error
	Close() error
}

type pendingApproval struct {
	PublicID string `json:"approvalId"`
	Kind     string `json:"kind"`
	Summary  string `json:"summary"`
	handle   string
	timer    *time.Timer
}

type eventStore struct {
	events []protocol.Frame
	bytes  int
	next   uint64
}

func (s *eventStore) append(f protocol.Frame) protocol.Frame {
	s.next++
	f.Sequence = s.next
	size := len(f.Payload) + 128
	s.events = append(s.events, f)
	s.bytes += size
	for len(s.events) > MaxEvents || s.bytes > MaxEventBytes {
		s.bytes -= len(s.events[0].Payload) + 128
		s.events = s.events[1:]
	}
	return f
}

type Controller struct {
	nodeID          string
	runtime         Runtime
	approvalTimeout time.Duration

	mu                sync.Mutex
	store             eventStore
	active            bool
	pending           map[string]*pendingApproval
	endpoint          transport.Endpoint
	stopAfterTerminal bool
	terminal          bool
}

func New(nodeID string, runtime Runtime) *Controller {
	return &Controller{nodeID: nodeID, runtime: runtime, approvalTimeout: DefaultApprovalTimeout, pending: make(map[string]*pendingApproval)}
}

func (c *Controller) SetApprovalTimeoutForTest(d time.Duration) { c.approvalTimeout = d }

// StopAfterTerminal makes archive-on-close validation exit after its one bounded Turn.
func (c *Controller) StopAfterTerminal() { c.mu.Lock(); c.stopAfterTerminal = true; c.mu.Unlock() }

func (c *Controller) Run(ctx context.Context, endpoint transport.Endpoint) error {
	if endpoint == nil || c.runtime == nil {
		return errors.New("PoC Node requires transport and runtime")
	}
	c.mu.Lock()
	c.endpoint = endpoint
	c.mu.Unlock()
	defer endpoint.Close()
	if err := c.emit(ctx, protocol.NodeStatus, "", map[string]any{"online": true}); err != nil {
		return err
	}
	for {
		frame, err := endpoint.Receive(ctx)
		if err != nil {
			c.mu.Lock()
			stopped := c.stopAfterTerminal && c.terminal
			c.mu.Unlock()
			if stopped {
				return nil
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		if frame.NodeID != "" && frame.NodeID != c.nodeID {
			_ = c.emit(ctx, protocol.ErrorEvent, frame.RequestID, map[string]string{"code": "wrong_node"})
			continue
		}
		switch frame.Type {
		case protocol.TaskStart:
			payload, e := protocol.DecodePayload[protocol.TaskStartPayload](frame)
			if e != nil || payload.WorkspaceID != protocol.WorkspaceID || payload.Prompt == "" {
				_ = c.emit(ctx, protocol.ErrorEvent, frame.RequestID, map[string]string{"code": "invalid_task"})
				continue
			}
			if err := c.start(ctx, frame.RequestID, payload.Prompt); err != nil {
				_ = c.emit(ctx, protocol.ErrorEvent, frame.RequestID, map[string]string{"code": errorCode(err)})
			}
		case protocol.ApprovalResolve:
			payload, e := protocol.DecodePayload[protocol.ApprovalResolvePayload](frame)
			if e != nil || (payload.Decision != "accept" && payload.Decision != "decline") {
				_ = c.emit(ctx, protocol.ErrorEvent, frame.RequestID, map[string]string{"code": "invalid_approval"})
				continue
			}
			if err := c.resolve(ctx, frame.RequestID, payload.ApprovalID, payload.Decision); err != nil {
				_ = c.emit(ctx, protocol.ErrorEvent, frame.RequestID, map[string]string{"code": errorCode(err)})
			}
		case protocol.EventsResume:
			payload, e := protocol.DecodePayload[protocol.ResumePayload](frame)
			if e != nil {
				_ = c.emit(ctx, protocol.ErrorEvent, frame.RequestID, map[string]string{"code": "invalid_resume"})
				continue
			}
			if err := c.resume(ctx, frame.RequestID, payload.LastSequence); err != nil {
				return err
			}
		default:
			_ = c.emit(ctx, protocol.ErrorEvent, frame.RequestID, map[string]string{"code": "unsupported_control"})
		}
	}
}

func (c *Controller) start(ctx context.Context, requestID, prompt string) error {
	c.mu.Lock()
	if c.active {
		c.mu.Unlock()
		return ErrActiveTurn
	}
	c.active = true
	c.mu.Unlock()
	events, err := c.runtime.Start(ctx, prompt)
	if err != nil {
		c.mu.Lock()
		c.active = false
		c.mu.Unlock()
		return err
	}
	_ = c.emit(ctx, protocol.ThreadStarted, requestID, map[string]any{"status": "ready"})
	_ = c.emit(ctx, protocol.TurnStarted, requestID, map[string]any{"status": "active"})
	go c.consume(ctx, requestID, events)
	return nil
}

func (c *Controller) consume(ctx context.Context, requestID string, events <-chan RuntimeEvent) {
	for {
		select {
		case event, ok := <-events:
			if !ok {
				c.finish(ctx, requestID, protocol.Ambiguous, map[string]string{"status": "unknown"})
				return
			}
			if event.Approval != nil {
				c.addApproval(ctx, requestID, *event.Approval)
				continue
			}
			kind := event.Type
			if event.Ambiguous {
				kind = protocol.Ambiguous
			}
			if kind == "" {
				kind = protocol.ErrorEvent
			}
			_ = c.emit(ctx, kind, requestID, event.Payload)
			if event.Terminal {
				c.finishState()
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

func (c *Controller) addApproval(ctx context.Context, requestID string, approval RuntimeApproval) {
	id, err := randomID(16)
	if err != nil {
		c.finish(ctx, requestID, protocol.Ambiguous, map[string]string{"status": "unknown"})
		return
	}
	pending := &pendingApproval{PublicID: id, Kind: approval.Kind, Summary: approval.Summary, handle: approval.Handle}
	c.mu.Lock()
	c.pending[id] = pending
	c.mu.Unlock()
	pending.timer = time.AfterFunc(c.approvalTimeout, func() { _ = c.resolve(context.Background(), requestID, id, "decline") })
	_ = c.emit(ctx, protocol.ApprovalRequested, requestID, pending)
}

func (c *Controller) resolve(ctx context.Context, requestID, id, decision string) error {
	c.mu.Lock()
	pending, ok := c.pending[id]
	if ok {
		delete(c.pending, id)
	}
	c.mu.Unlock()
	if !ok {
		return ErrUnknownApproval
	}
	if pending.timer != nil {
		pending.timer.Stop()
	}
	if err := c.runtime.Resolve(ctx, pending.handle, decision); err != nil {
		return err
	}
	return c.emit(ctx, protocol.ApprovalResolved, requestID, map[string]string{"approvalId": id, "decision": decision})
}

func (c *Controller) resume(ctx context.Context, requestID string, last uint64) error {
	c.mu.Lock()
	events := append([]protocol.Frame(nil), c.store.events...)
	next := c.store.next
	active := c.active
	pending := make([]pendingApproval, 0, len(c.pending))
	for _, p := range c.pending {
		pending = append(pending, *p)
	}
	c.mu.Unlock()
	if len(events) > 0 && last+1 < events[0].Sequence {
		if err := c.send(ctx, mustFrame(protocol.HistoryGap, requestID, c.nodeID, map[string]uint64{"oldestSequence": events[0].Sequence, "latestSequence": next})); err != nil {
			return err
		}
		return c.send(ctx, mustFrame(protocol.Snapshot, requestID, c.nodeID, map[string]any{"latestSequence": next, "active": active, "pendingApprovals": pending}))
	}
	for _, f := range events {
		if f.Sequence > last {
			if err := c.send(ctx, f); err != nil {
				return err
			}
		}
	}
	snapshot := mustFrame(protocol.Snapshot, requestID, c.nodeID, map[string]any{"latestSequence": next, "active": active, "pendingApprovals": pending})
	snapshot.Sequence = next
	return c.send(ctx, snapshot)
}

func (c *Controller) finish(ctx context.Context, requestID, kind string, payload any) {
	_ = c.emit(ctx, kind, requestID, payload)
	c.finishState()
}
func (c *Controller) finishState() {
	c.mu.Lock()
	c.active = false
	c.terminal = true
	stop, endpoint := c.stopAfterTerminal, c.endpoint
	c.mu.Unlock()
	if stop && endpoint != nil {
		_ = endpoint.Close()
	}
}

func (c *Controller) emit(ctx context.Context, kind, requestID string, payload any) error {
	f, err := protocol.New(kind, requestID, c.nodeID, payload)
	if err != nil {
		return err
	}
	c.mu.Lock()
	f = c.store.append(f)
	c.mu.Unlock()
	return c.send(ctx, f)
}
func (c *Controller) send(ctx context.Context, f protocol.Frame) error {
	c.mu.Lock()
	ep := c.endpoint
	c.mu.Unlock()
	if ep == nil {
		return transport.ErrClosed
	}
	return ep.Send(ctx, f)
}
func mustFrame(kind, req, node string, p any) protocol.Frame {
	f, _ := protocol.New(kind, req, node, p)
	return f
}
func errorCode(err error) string {
	switch {
	case errors.Is(err, ErrActiveTurn):
		return "active_turn"
	case errors.Is(err, ErrUnknownApproval):
		return "unknown_approval"
	default:
		return "runtime_error"
	}
}
func randomID(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// SnapshotJSON exists for bounded diagnostic tests; it never includes prompts or runtime handles.
func (c *Controller) SnapshotJSON() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	v := map[string]any{"nodeId": c.nodeID, "sequence": c.store.next, "active": c.active, "pendingCount": len(c.pending)}
	b, _ := json.Marshal(v)
	return b
}

func (c *Controller) Close() error {
	c.mu.Lock()
	for _, p := range c.pending {
		if p.timer != nil {
			p.timer.Stop()
		}
	}
	c.mu.Unlock()
	return c.runtime.Close()
}
