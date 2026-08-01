package appserver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yuanshu-ai/yuanshu/internal/platform"
)

const defaultCloseTimeout = 3 * time.Second

type Options struct {
	Processes       platform.ProcessManager
	Spec            platform.ProcessSpec
	MaxMessageBytes int
	QueueSize       int
	CloseTimeout    time.Duration
}

type callResult struct {
	result json.RawMessage
	err    error
}

type inboundMessage struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *RPCError       `json:"error,omitempty"`
}

type Client struct {
	process      platform.Process
	stdin        io.WriteCloser
	stdout       io.ReadCloser
	stderr       io.ReadCloser
	maxMessage   int
	closeTimeout time.Duration

	writeMu sync.Mutex
	nextID  atomic.Int64

	pendingMu sync.Mutex
	pending   map[int64]chan callResult
	messages  chan Message
	done      chan struct{}

	terminalMu  sync.RWMutex
	terminalErr error
	closing     atomic.Bool
	finishOnce  sync.Once
	closeOnce   sync.Once
	closeErr    error
}

func Start(ctx context.Context, options Options) (*Client, error) {
	if ctx == nil {
		return nil, context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if options.Processes == nil || !options.Processes.Available() || options.Spec.Executable == "" {
		return nil, platform.ErrUnavailable
	}
	maxMessage := options.MaxMessageBytes
	if maxMessage <= 0 {
		maxMessage = DefaultMaxMessageBytes
	}
	queueSize := options.QueueSize
	if queueSize <= 0 {
		queueSize = DefaultQueueSize
	}
	closeTimeout := options.CloseTimeout
	if closeTimeout <= 0 {
		closeTimeout = defaultCloseTimeout
	}
	process, err := options.Processes.Start(ctx, options.Spec)
	if err != nil {
		return nil, err
	}
	client := &Client{
		process:      process,
		stdin:        process.Stdin(),
		stdout:       process.Stdout(),
		stderr:       process.Stderr(),
		maxMessage:   maxMessage,
		closeTimeout: closeTimeout,
		pending:      make(map[int64]chan callResult),
		messages:     make(chan Message, queueSize),
		done:         make(chan struct{}),
	}
	go func() { _, _ = io.Copy(io.Discard, client.stderr) }()
	go client.run()
	return client, nil
}

func (c *Client) Initialize(ctx context.Context, info ClientInfo) (InitializeResult, error) {
	var result InitializeResult
	if err := c.Call(ctx, "initialize", struct {
		ClientInfo ClientInfo `json:"clientInfo"`
	}{ClientInfo: info}, &result); err != nil {
		return InitializeResult{}, err
	}
	if err := c.Notify("initialized", struct{}{}); err != nil {
		return InitializeResult{}, err
	}
	return result, nil
}

func (c *Client) Call(ctx context.Context, method string, params, result any) error {
	if ctx == nil {
		return context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if method == "" {
		return ErrInvalidMessage
	}
	id := c.nextID.Add(1)
	reply := make(chan callResult, 1)
	c.pendingMu.Lock()
	if c.isDone() {
		c.pendingMu.Unlock()
		return c.closedError()
	}
	c.pending[id] = reply
	c.pendingMu.Unlock()
	message := struct {
		Method string `json:"method"`
		ID     int64  `json:"id"`
		Params any    `json:"params"`
	}{method, id, params}
	if err := c.write(message); err != nil {
		c.removePending(id)
		return err
	}
	select {
	case response := <-reply:
		if response.err != nil {
			return response.err
		}
		if result == nil || len(bytes.TrimSpace(response.result)) == 0 || bytes.Equal(bytes.TrimSpace(response.result), []byte("null")) {
			return nil
		}
		if json.Unmarshal(response.result, result) != nil {
			return fmt.Errorf("decode %s response: %w", method, ErrInvalidMessage)
		}
		return nil
	case <-ctx.Done():
		c.removePending(id)
		return ctx.Err()
	case <-c.done:
		return c.closedError()
	}
}

func (c *Client) Notify(method string, params any) error {
	if method == "" {
		return ErrInvalidMessage
	}
	return c.write(struct {
		Method string `json:"method"`
		Params any    `json:"params"`
	}{method, params})
}

func (c *Client) Respond(id RequestID, result any, rpcErr *RPCError) error {
	if len(id.raw) == 0 {
		return ErrInvalidMessage
	}
	if rpcErr != nil {
		return c.write(struct {
			ID    RequestID `json:"id"`
			Error *RPCError `json:"error"`
		}{id, rpcErr})
	}
	return c.write(struct {
		ID     RequestID `json:"id"`
		Result any       `json:"result"`
	}{id, result})
}

func (c *Client) Messages() <-chan Message { return c.messages }
func (c *Client) Done() <-chan struct{}    { return c.done }

func (c *Client) Err() error {
	c.terminalMu.RLock()
	defer c.terminalMu.RUnlock()
	return c.terminalErr
}

func (c *Client) Close() error {
	c.closeOnce.Do(func() {
		c.closing.Store(true)
		_ = c.stdin.Close()
		timer := time.NewTimer(c.closeTimeout)
		defer timer.Stop()
		select {
		case <-c.done:
			c.closeErr = c.Err()
		case <-timer.C:
			stopCtx, cancel := context.WithTimeout(context.Background(), c.closeTimeout)
			_, stopErr := c.process.Stop(stopCtx)
			cancel()
			<-c.done
			if stopErr != nil && !errors.Is(stopErr, platform.ErrProcessStopped) {
				c.closeErr = platform.ErrUnavailable
			}
		}
		if errors.Is(c.closeErr, ErrClosed) || errors.Is(c.closeErr, ErrProcessExited) && c.closing.Load() {
			c.closeErr = nil
		}
	})
	return c.closeErr
}

func (c *Client) run() {
	readErr := c.readLoop()
	exit, waitErr := c.process.Wait(context.Background())
	terminal := readErr
	if c.closing.Load() {
		terminal = nil
	}
	if terminal == nil && !c.closing.Load() && (waitErr != nil || exit.Code != 0) {
		terminal = ErrProcessExited
	}
	c.finish(terminal)
}

func (c *Client) readLoop() error {
	reader := bufio.NewReaderSize(c.stdout, 64<<10)
	for {
		line, err := readLimitedLine(reader, c.maxMessage)
		if len(bytes.TrimSpace(line)) > 0 {
			if handleErr := c.handleLine(line); handleErr != nil {
				stopCtx, cancel := context.WithTimeout(context.Background(), c.closeTimeout)
				_, _ = c.process.Stop(stopCtx)
				cancel()
				return handleErr
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) || c.closing.Load() {
				return nil
			}
			return ErrInvalidMessage
		}
	}
}

func (c *Client) handleLine(line []byte) error {
	var incoming inboundMessage
	if json.Unmarshal(line, &incoming) != nil {
		return ErrInvalidMessage
	}
	if len(incoming.ID) > 0 && incoming.Method == "" {
		var id int64
		if json.Unmarshal(incoming.ID, &id) != nil {
			return ErrInvalidMessage
		}
		c.pendingMu.Lock()
		pending, ok := c.pending[id]
		if ok {
			delete(c.pending, id)
		}
		c.pendingMu.Unlock()
		if !ok {
			return nil
		}
		if incoming.Error != nil {
			pending <- callResult{err: incoming.Error}
		} else {
			pending <- callResult{result: append(json.RawMessage(nil), incoming.Result...)}
		}
		return nil
	}
	if incoming.Method == "" {
		return ErrInvalidMessage
	}
	message := Message{Method: incoming.Method, Params: append(json.RawMessage(nil), incoming.Params...)}
	if len(incoming.ID) > 0 {
		id, err := parseRequestID(incoming.ID)
		if err != nil {
			return err
		}
		message.ID = &id
	}
	select {
	case c.messages <- message:
		return nil
	default:
		return ErrQueueFull
	}
}

func (c *Client) write(message any) error {
	data, err := json.Marshal(message)
	if err != nil {
		return ErrInvalidMessage
	}
	if len(data)+1 > c.maxMessage {
		return ErrMessageTooLarge
	}
	data = append(data, '\n')
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.isDone() {
		return c.closedError()
	}
	if _, err := c.stdin.Write(data); err != nil {
		return ErrProcessExited
	}
	return nil
}

func (c *Client) finish(err error) {
	c.finishOnce.Do(func() {
		c.terminalMu.Lock()
		c.terminalErr = err
		c.terminalMu.Unlock()
		c.pendingMu.Lock()
		for id, pending := range c.pending {
			delete(c.pending, id)
			pending <- callResult{err: c.closedError()}
		}
		c.pendingMu.Unlock()
		close(c.messages)
		close(c.done)
	})
}

func (c *Client) removePending(id int64) {
	c.pendingMu.Lock()
	delete(c.pending, id)
	c.pendingMu.Unlock()
}

func (c *Client) isDone() bool {
	select {
	case <-c.done:
		return true
	default:
		return false
	}
}

func (c *Client) closedError() error {
	if err := c.Err(); err != nil {
		return err
	}
	return ErrClosed
}

func readLimitedLine(reader *bufio.Reader, limit int) ([]byte, error) {
	var line []byte
	for {
		fragment, err := reader.ReadSlice('\n')
		if len(line)+len(fragment) > limit {
			return nil, ErrMessageTooLarge
		}
		line = append(line, fragment...)
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return bytes.TrimSuffix(line, []byte{'\n'}), err
	}
}
