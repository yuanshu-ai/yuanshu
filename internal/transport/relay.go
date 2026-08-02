package transport

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

const defaultRelayWriteTimeout = 5 * time.Second

// RelayOptions configures one already-authenticated WebSocket connection.
type RelayOptions struct {
	QueueCapacity     int
	MaxSendBytes      int
	MaxReceiveBytes   int
	WriteTimeout      time.Duration
	HeartbeatInterval time.Duration
	IdleTimeout       time.Duration
}

// NewRelay wraps an authenticated WebSocket without parsing or re-encoding
// Protocol frames.
func NewRelay(conn *websocket.Conn, options RelayOptions) (Transport, error) {
	if conn == nil || options.QueueCapacity < 0 || options.MaxSendBytes < 1 || options.MaxReceiveBytes < 1 || options.WriteTimeout < 0 || options.HeartbeatInterval < 0 || options.IdleTimeout < 0 {
		return nil, errors.New("relay transport options are invalid")
	}
	capacity := options.QueueCapacity
	if capacity == 0 {
		capacity = DefaultQueueCapacity
	}
	writeTimeout := options.WriteTimeout
	if writeTimeout == 0 {
		writeTimeout = defaultRelayWriteTimeout
	}
	heartbeat := options.HeartbeatInterval
	if heartbeat == 0 {
		heartbeat = 30 * time.Second
	}
	idle := options.IdleTimeout
	if idle == 0 {
		idle = 90 * time.Second
	}
	if idle <= heartbeat {
		return nil, errors.New("relay transport options are invalid")
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := &relayTransport{
		conn: conn, maxSend: options.MaxSendBytes, writeTimeout: writeTimeout, heartbeatInterval: heartbeat, idleTimeout: idle,
		ctx: ctx, cancel: cancel, send: make(chan Frame, capacity), receive: make(chan relayRead, capacity), done: make(chan struct{}),
	}
	conn.SetReadLimit(int64(options.MaxReceiveBytes))
	result.lastActivity.Store(time.Now().UnixNano())
	result.workers.Add(3)
	go result.readLoop()
	go result.writeLoop()
	go result.heartbeatLoop()
	return result, nil
}

type relayRead struct {
	frame Frame
	err   error
}

type relayTransport struct {
	conn              *websocket.Conn
	maxSend           int
	writeTimeout      time.Duration
	heartbeatInterval time.Duration
	idleTimeout       time.Duration
	lastActivity      atomic.Int64
	ctx               context.Context
	cancel            context.CancelFunc
	send              chan Frame
	receive           chan relayRead
	done              chan struct{}
	workers           sync.WaitGroup
	mu                sync.Mutex
	closing           bool
	closed            bool
	finalErr          error
	terminateOne      sync.Once
}

var _ Transport = (*relayTransport)(nil)

func (r *relayTransport) Send(ctx context.Context, frame Frame) error {
	if ctx == nil {
		return context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if frame.Len() > r.maxSend {
		return ErrFrameTooLarge
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closing || r.closed {
		return ErrClosed
	}
	select {
	case r.send <- NewFrame(frame.Bytes()):
		return nil
	default:
		return ErrBackpressure
	}
}

func (r *relayTransport) Receive(ctx context.Context) (Frame, error) {
	if ctx == nil {
		return Frame{}, context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return Frame{}, err
	}
	select {
	case item := <-r.receive:
		return item.frame, item.err
	default:
	}
	select {
	case <-ctx.Done():
		return Frame{}, ctx.Err()
	case item := <-r.receive:
		return item.frame, item.err
	case <-r.done:
		select {
		case item := <-r.receive:
			return item.frame, item.err
		default:
			return Frame{}, r.terminalError()
		}
	}
}

func (r *relayTransport) Close() error {
	r.mu.Lock()
	if !r.closing && !r.closed {
		r.closing = true
		close(r.send)
	}
	r.mu.Unlock()
	r.workers.Wait()
	return nil
}

func (r *relayTransport) readLoop() {
	defer r.workers.Done()
	for {
		messageType, raw, err := r.conn.Read(r.ctx)
		if err != nil {
			if errors.Is(err, websocket.ErrMessageTooBig) {
				r.terminate(ErrFrameTooLarge)
			} else {
				r.terminate(ErrClosed)
			}
			return
		}
		if messageType != websocket.MessageText && messageType != websocket.MessageBinary {
			continue
		}
		r.lastActivity.Store(time.Now().UnixNano())
		select {
		case r.receive <- relayRead{frame: NewFrame(raw)}:
		case <-r.done:
			return
		default:
			r.terminate(ErrBackpressure)
			return
		}
	}
}

func (r *relayTransport) writeLoop() {
	defer r.workers.Done()
	for {
		select {
		case <-r.done:
			return
		case frame, ok := <-r.send:
			if !ok {
				_ = r.conn.Close(websocket.StatusNormalClosure, "closed")
				r.terminate(ErrClosed)
				return
			}
			ctx, cancel := context.WithTimeout(r.ctx, r.writeTimeout)
			err := r.conn.Write(ctx, websocket.MessageText, frame.Bytes())
			cancel()
			if err != nil {
				r.terminate(ErrClosed)
				return
			}
			r.lastActivity.Store(time.Now().UnixNano())
		}
	}
}

func (r *relayTransport) heartbeatLoop() {
	defer r.workers.Done()
	ticker := time.NewTicker(r.heartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-r.done:
			return
		case now := <-ticker.C:
			last := time.Unix(0, r.lastActivity.Load())
			if now.Sub(last) >= r.idleTimeout {
				r.terminate(ErrClosed)
				return
			}
			ctx, cancel := context.WithTimeout(r.ctx, r.writeTimeout)
			err := r.conn.Ping(ctx)
			cancel()
			if err != nil {
				r.terminate(ErrClosed)
				return
			}
			r.lastActivity.Store(time.Now().UnixNano())
		}
	}
}

func (r *relayTransport) terminate(err error) {
	r.terminateOne.Do(func() {
		r.mu.Lock()
		r.closed = true
		if err == nil {
			err = ErrClosed
		}
		r.finalErr = err
		r.mu.Unlock()
		r.cancel()
		_ = r.conn.CloseNow()
		close(r.done)
	})
}

func (r *relayTransport) terminalError() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.finalErr != nil {
		return r.finalErr
	}
	return ErrClosed
}
