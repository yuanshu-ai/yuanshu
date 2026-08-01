package probe

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"
)

const (
	defaultCloseTimeout = 2 * time.Second
	defaultStderrLimit  = 64 << 10
)

// Options controls the app-server subprocess used by the probe.
type Options struct {
	Binary          string
	Args            []string
	Dir             string
	Env             []string
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

type limitedBuffer struct {
	mu        sync.Mutex
	buffer    bytes.Buffer
	remaining int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	written := len(p)
	if b.remaining <= 0 {
		return written, nil
	}
	if len(p) > b.remaining {
		p = p[:b.remaining]
	}
	_, _ = b.buffer.Write(p)
	b.remaining -= len(p)
	return written, nil
}

func (b *limitedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}

// Client is a concurrency-safe JSONL client for one app-server subprocess.
type Client struct {
	cmd          *exec.Cmd
	stdin        io.WriteCloser
	stdout       io.ReadCloser
	maxMessage   int
	closeTimeout time.Duration
	stderr       *limitedBuffer

	writeMu sync.Mutex
	nextID  atomic.Int64

	pendingMu sync.Mutex
	pending   map[int64]chan callResult

	messages chan Message
	done     chan struct{}

	terminalMu  sync.Mutex
	terminalErr error

	closeOnce sync.Once
	closeErr  error
}

// Start launches a Codex app-server subprocess. It does not initialize the connection.
func Start(ctx context.Context, options Options) (*Client, error) {
	if ctx == nil {
		return nil, errors.New("start codex probe: nil context")
	}

	binary := options.Binary
	if binary == "" {
		binary = "codex"
	}
	args := append([]string(nil), options.Args...)
	if len(args) == 0 {
		args = []string{"app-server", "--stdio"}
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

	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Dir = options.Dir
	if options.Env != nil {
		cmd.Env = append([]string(nil), options.Env...)
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("open codex app-server stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("open codex app-server stdout: %w", err)
	}
	stderr := &limitedBuffer{remaining: defaultStderrLimit}
	cmd.Stderr = stderr

	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, fmt.Errorf("start codex app-server: %w", err)
	}

	client := &Client{
		cmd:          cmd,
		stdin:        stdin,
		stdout:       stdout,
		maxMessage:   maxMessage,
		closeTimeout: closeTimeout,
		stderr:       stderr,
		pending:      make(map[int64]chan callResult),
		messages:     make(chan Message, queueSize),
		done:         make(chan struct{}),
	}
	client.nextID.Store(0)
	go client.readLoop()
	return client, nil
}

// Initialize performs the mandatory initialize/initialized handshake without experimental capabilities.
func (c *Client) Initialize(ctx context.Context, info ClientInfo) (InitializeResult, error) {
	var result InitializeResult
	params := struct {
		ClientInfo ClientInfo `json:"clientInfo"`
	}{ClientInfo: info}
	if err := c.Call(ctx, "initialize", params, &result); err != nil {
		return InitializeResult{}, err
	}
	if err := c.Notify("initialized", struct{}{}); err != nil {
		return InitializeResult{}, err
	}
	return result, nil
}

// Call sends a request and waits for its matching response.
func (c *Client) Call(ctx context.Context, method string, params any, result any) error {
	if ctx == nil {
		return errors.New("codex app-server call: nil context")
	}
	if method == "" {
		return errors.New("codex app-server call: empty method")
	}

	id := c.nextID.Add(1)
	response := make(chan callResult, 1)
	c.pendingMu.Lock()
	if c.isDone() {
		c.pendingMu.Unlock()
		return c.closedError()
	}
	c.pending[id] = response
	c.pendingMu.Unlock()

	message := struct {
		Method string `json:"method"`
		ID     int64  `json:"id"`
		Params any    `json:"params"`
	}{Method: method, ID: id, Params: params}
	if err := c.write(message); err != nil {
		c.removePending(id)
		return err
	}

	select {
	case reply := <-response:
		if reply.err != nil {
			return reply.err
		}
		if result == nil || len(reply.result) == 0 || bytes.Equal(bytes.TrimSpace(reply.result), []byte("null")) {
			return nil
		}
		if err := json.Unmarshal(reply.result, result); err != nil {
			return fmt.Errorf("decode %s response: %w", method, err)
		}
		return nil
	case <-ctx.Done():
		return fmt.Errorf("wait for %s response: %w", method, ctx.Err())
	case <-c.done:
		select {
		case reply := <-response:
			return reply.err
		default:
			return c.closedError()
		}
	}
}

// Notify sends a notification without a request ID.
func (c *Client) Notify(method string, params any) error {
	if method == "" {
		return errors.New("codex app-server notification: empty method")
	}
	return c.write(struct {
		Method string `json:"method"`
		Params any    `json:"params"`
	}{Method: method, Params: params})
}

// Respond answers a server-initiated request and preserves its ID representation.
func (c *Client) Respond(id RequestID, result any, rpcErr *RPCError) error {
	if len(id.raw) == 0 {
		return fmt.Errorf("respond to codex app-server: %w", ErrInvalidMessage)
	}
	if rpcErr != nil {
		return c.write(struct {
			ID    RequestID `json:"id"`
			Error *RPCError `json:"error"`
		}{ID: id, Error: rpcErr})
	}
	return c.write(struct {
		ID     RequestID `json:"id"`
		Result any       `json:"result"`
	}{ID: id, Result: result})
}

// Messages returns server notifications and server-initiated requests.
func (c *Client) Messages() <-chan Message {
	return c.messages
}

// Done is closed after the subprocess and reader have terminated.
func (c *Client) Done() <-chan struct{} {
	return c.done
}

// Wait blocks until the client terminates and returns its final error.
func (c *Client) Wait() error {
	<-c.done
	return c.Err()
}

// Err returns the final client error after Done is closed.
func (c *Client) Err() error {
	c.terminalMu.Lock()
	defer c.terminalMu.Unlock()
	return c.terminalErr
}

// Stderr returns bounded and redacted diagnostic output.
func (c *Client) Stderr() string {
	return RedactText(c.stderr.String())
}

// Close asks app-server to exit, then kills it after the configured timeout.
// Repeated calls return the same result.
func (c *Client) Close() error {
	c.closeOnce.Do(func() {
		_ = c.stdin.Close()
		timer := time.NewTimer(c.closeTimeout)
		defer timer.Stop()
		select {
		case <-c.done:
			c.closeErr = c.Err()
		case <-timer.C:
			if c.cmd.Process != nil {
				_ = c.cmd.Process.Kill()
			}
			<-c.done
			c.closeErr = c.Err()
		}
	})
	return c.closeErr
}

func (c *Client) write(message any) error {
	data, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("encode codex app-server message: %w", err)
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
		return fmt.Errorf("write codex app-server message: %w", err)
	}
	return nil
}

func (c *Client) readLoop() {
	reader := bufio.NewReaderSize(c.stdout, 64<<10)
	var terminalErr error
	for {
		line, err := readLimitedLine(reader, c.maxMessage)
		if len(bytes.TrimSpace(line)) > 0 {
			if handleErr := c.handleLine(line); handleErr != nil {
				terminalErr = handleErr
				if c.cmd.Process != nil {
					_ = c.cmd.Process.Kill()
				}
				break
			}
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				terminalErr = err
			}
			break
		}
	}

	waitErr := c.cmd.Wait()
	if terminalErr == nil && waitErr != nil && !c.isExpectedClose() {
		terminalErr = fmt.Errorf("codex app-server exited: %w", waitErr)
	}
	c.finish(terminalErr)
}

func (c *Client) handleLine(line []byte) error {
	var incoming inboundMessage
	if err := json.Unmarshal(line, &incoming); err != nil {
		return fmt.Errorf("%w: decode JSON: %v", ErrInvalidMessage, err)
	}

	if len(incoming.ID) > 0 && incoming.Method == "" {
		var id int64
		if err := json.Unmarshal(incoming.ID, &id); err != nil {
			return fmt.Errorf("%w: response id is not an integer", ErrInvalidMessage)
		}
		c.pendingMu.Lock()
		pending, ok := c.pending[id]
		if ok {
			delete(c.pending, id)
		}
		c.pendingMu.Unlock()
		if !ok {
			return nil // A timed-out caller may receive its late response.
		}
		if incoming.Error != nil {
			pending <- callResult{err: incoming.Error}
		} else {
			pending <- callResult{result: append(json.RawMessage(nil), incoming.Result...)}
		}
		return nil
	}

	if incoming.Method == "" {
		return fmt.Errorf("%w: message has neither response nor method", ErrInvalidMessage)
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

func (c *Client) finish(err error) {
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

func (c *Client) isExpectedClose() bool {
	return c.stdin == nil || c.isDone() || errors.Is(c.cmd.Err, context.Canceled)
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

// Environment returns an inherited environment when Options.Env is not otherwise customized.
func Environment() []string {
	return os.Environ()
}
