package transport

import (
	"context"
	"errors"
	"sync"

	protocolv1 "github.com/yuanshu-ai/yuanshu/internal/protocol/v1"
)

// DefaultQueueCapacity is the per-direction standalone frame capacity.
const DefaultQueueCapacity = 64

// StandaloneOptions configures an in-process Server-to-Node pair.
type StandaloneOptions struct {
	// QueueCapacity applies independently to each direction. Zero uses the default.
	QueueCapacity int
}

// NewStandalonePair creates the Server and Node sides of the in-process
// transport. The Server sends control frames and the Node sends event frames.
func NewStandalonePair(options StandaloneOptions) (serverSide Transport, nodeSide Transport, err error) {
	capacity := options.QueueCapacity
	if capacity < 0 {
		return nil, nil, errors.New("standalone transport queue capacity cannot be negative")
	}
	if capacity == 0 {
		capacity = DefaultQueueCapacity
	}

	serverToNode := newDirection(capacity)
	nodeToServer := newDirection(capacity)
	server := newEndpoint(nodeToServer, serverToNode, protocolv1.ControlFrameMaxBytes)
	node := newEndpoint(serverToNode, nodeToServer, protocolv1.EventFrameMaxBytes)
	return server, node, nil
}

type direction struct {
	mu             sync.Mutex
	queue          chan Frame
	senderClosed   bool
	receiverClosed bool
}

func newDirection(capacity int) *direction {
	return &direction{queue: make(chan Frame, capacity)}
}

func (d *direction) send(frame Frame) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.senderClosed || d.receiverClosed {
		return ErrClosed
	}
	select {
	case d.queue <- frame:
		return nil
	default:
		return ErrBackpressure
	}
}

func (d *direction) closeSender() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.senderClosed {
		return
	}
	d.senderClosed = true
	close(d.queue)
}

func (d *direction) closeReceiver() {
	d.mu.Lock()
	d.receiverClosed = true
	d.mu.Unlock()
}

type endpoint struct {
	in      *direction
	out     *direction
	maxSend int
	done    chan struct{}
	once    sync.Once
}

var _ Transport = (*endpoint)(nil)

func newEndpoint(in, out *direction, maxSend int) *endpoint {
	return &endpoint{in: in, out: out, maxSend: maxSend, done: make(chan struct{})}
}

func (e *endpoint) Send(ctx context.Context, frame Frame) error {
	if ctx == nil {
		return context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-e.done:
		return ErrClosed
	default:
	}
	if frame.Len() > e.maxSend {
		return ErrFrameTooLarge
	}
	return e.out.send(frame)
}

func (e *endpoint) Receive(ctx context.Context) (Frame, error) {
	if ctx == nil {
		return Frame{}, context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return Frame{}, err
	}
	select {
	case <-e.done:
		return Frame{}, ErrClosed
	default:
	}
	select {
	case <-ctx.Done():
		return Frame{}, ctx.Err()
	case <-e.done:
		return Frame{}, ErrClosed
	case frame, ok := <-e.in.queue:
		if !ok {
			return Frame{}, ErrClosed
		}
		return frame, nil
	}
}

func (e *endpoint) Close() error {
	e.once.Do(func() {
		close(e.done)
		e.out.closeSender()
		e.in.closeReceiver()
	})
	return nil
}
