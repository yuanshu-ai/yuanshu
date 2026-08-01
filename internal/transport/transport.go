package transport

import (
	"context"
	"errors"
)

var (
	ErrClosed        = errors.New("transport is closed")
	ErrBackpressure  = errors.New("transport queue is full")
	ErrFrameTooLarge = errors.New("transport frame exceeds direction limit")
)

// Transport is one full-duplex connection. Reliability, reconnection,
// acknowledgements, replay, and persistence belong to higher layers.
type Transport interface {
	Send(ctx context.Context, frame Frame) error
	Receive(ctx context.Context) (Frame, error)
	Close() error
}
