// Package transport contains temporary transports used only by the M0 PoC.
package transport

import (
	"context"
	"errors"
	"sync"

	"github.com/yuanshu-ai/yuanshu/internal/poc/protocol"
)

var (
	ErrClosed    = errors.New("PoC transport closed")
	ErrQueueFull = errors.New("PoC transport queue is full")
)

type Endpoint interface {
	Send(context.Context, protocol.Frame) error
	Receive(context.Context) (protocol.Frame, error)
	Close() error
}

type memoryEndpoint struct {
	in       <-chan protocol.Frame
	out      chan<- protocol.Frame
	done     chan struct{}
	peerDone <-chan struct{}
	once     sync.Once
}

// StandalonePair returns a bounded in-process transport pair. Server and Node
// still exchange the exact same frames used by the relay deployment.
func StandalonePair(capacity int) (Endpoint, Endpoint) {
	if capacity <= 0 {
		capacity = 64
	}
	aToB, bToA := make(chan protocol.Frame, capacity), make(chan protocol.Frame, capacity)
	aDone, bDone := make(chan struct{}), make(chan struct{})
	return &memoryEndpoint{in: bToA, out: aToB, done: aDone, peerDone: bDone}, &memoryEndpoint{in: aToB, out: bToA, done: bDone, peerDone: aDone}
}

func (m *memoryEndpoint) Send(ctx context.Context, f protocol.Frame) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-m.done:
		return ErrClosed
	case <-m.peerDone:
		return ErrClosed
	default:
	}
	select {
	case m.out <- f:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-m.done:
		return ErrClosed
	case <-m.peerDone:
		return ErrClosed
	default:
		return ErrQueueFull
	}
}
func (m *memoryEndpoint) Receive(ctx context.Context) (protocol.Frame, error) {
	select {
	case f := <-m.in:
		return f, nil
	case <-ctx.Done():
		return protocol.Frame{}, ctx.Err()
	case <-m.done:
		return protocol.Frame{}, ErrClosed
	case <-m.peerDone:
		return protocol.Frame{}, ErrClosed
	}
}
func (m *memoryEndpoint) Close() error { m.once.Do(func() { close(m.done) }); return nil }
