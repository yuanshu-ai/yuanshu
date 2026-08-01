package transport_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	protocolv1 "github.com/yuanshu-ai/yuanshu/internal/protocol/v1"
	transportpkg "github.com/yuanshu-ai/yuanshu/internal/transport"
)

type pairFactory func(capacity int) (serverSide transportpkg.Transport, nodeSide transportpkg.Transport, err error)

func TestTransportContract(t *testing.T) {
	factories := map[string]pairFactory{
		"standalone": func(capacity int) (transportpkg.Transport, transportpkg.Transport, error) {
			return transportpkg.NewStandalonePair(transportpkg.StandaloneOptions{QueueCapacity: capacity})
		},
		"relay fake": newRelayFakePair,
	}
	for name, factory := range factories {
		t.Run(name, func(t *testing.T) {
			runTransportContract(t, factory)
		})
	}
}

func runTransportContract(t *testing.T, factory pairFactory) {
	t.Helper()
	t.Run("opaque bidirectional frames", func(t *testing.T) {
		server, node := newPair(t, factory, 8)
		defer closePair(server, node)

		control := []byte(" \n{\"signature\":\"kept\",\"type\":\"turn.start\",\"payload\":{\"input\":\"x\"}}\t")
		malformed := []byte("{not-json")
		event := []byte("{\"type\":\"agent.message.delta\",\"payload\":{\"text\":\"hello\"}}")
		for _, raw := range [][]byte{control, malformed, {}} {
			if err := server.Send(context.Background(), transportpkg.NewFrame(raw)); err != nil {
				t.Fatal(err)
			}
			assertFrameBytes(t, mustReceive(t, node), raw)
		}
		if err := node.Send(context.Background(), transportpkg.NewFrame(event)); err != nil {
			t.Fatal(err)
		}
		assertFrameBytes(t, mustReceive(t, server), event)
	})

	t.Run("frame ownership isolation", func(t *testing.T) {
		server, node := newPair(t, factory, 2)
		defer closePair(server, node)

		raw := []byte("original")
		frame := transportpkg.NewFrame(raw)
		raw[0] = 'X'
		copyFromFrame := frame.Bytes()
		copyFromFrame[1] = 'X'
		if err := server.Send(context.Background(), frame); err != nil {
			t.Fatal(err)
		}
		received := mustReceive(t, node)
		firstCopy := received.Bytes()
		firstCopy[2] = 'X'
		assertFrameBytes(t, received, []byte("original"))
	})

	t.Run("fifo duplicates and concurrent safety", func(t *testing.T) {
		server, node := newPair(t, factory, 128)
		defer closePair(server, node)

		ordered := [][]byte{[]byte("first"), []byte("duplicate"), []byte("duplicate"), []byte("last")}
		for _, raw := range ordered {
			if err := server.Send(context.Background(), transportpkg.NewFrame(raw)); err != nil {
				t.Fatal(err)
			}
		}
		for _, expected := range ordered {
			assertFrameBytes(t, mustReceive(t, node), expected)
		}

		const concurrentFrames = 64
		errorsCh := make(chan error, concurrentFrames)
		for index := range concurrentFrames {
			go func() {
				raw := []byte(fmt.Sprintf("concurrent-%02d", index))
				errorsCh <- node.Send(context.Background(), transportpkg.NewFrame(raw))
			}()
		}
		for range concurrentFrames {
			if err := <-errorsCh; err != nil {
				t.Fatal(err)
			}
		}
		seen := make(map[string]bool, concurrentFrames)
		for range concurrentFrames {
			seen[string(mustReceive(t, server).Bytes())] = true
		}
		if len(seen) != concurrentFrames {
			t.Fatalf("received %d distinct concurrent frames, want %d", len(seen), concurrentFrames)
		}
	})

	t.Run("directional frame limits", func(t *testing.T) {
		server, node := newPair(t, factory, 2)
		defer closePair(server, node)

		controlAtLimit := bytes.Repeat([]byte{'c'}, protocolv1.ControlFrameMaxBytes)
		if err := server.Send(context.Background(), transportpkg.NewFrame(controlAtLimit)); err != nil {
			t.Fatalf("control at limit: %v", err)
		}
		assertFrameBytes(t, mustReceive(t, node), controlAtLimit)
		controlCanary := "control-frame-canary"
		controlTooLarge := append(bytes.Repeat([]byte{'x'}, protocolv1.ControlFrameMaxBytes+1), controlCanary...)
		err := server.Send(context.Background(), transportpkg.NewFrame(controlTooLarge))
		assertErrorIs(t, err, transportpkg.ErrFrameTooLarge)
		assertErrorSanitized(t, err, controlCanary)

		eventAtLimit := bytes.Repeat([]byte{'e'}, protocolv1.EventFrameMaxBytes)
		if err := node.Send(context.Background(), transportpkg.NewFrame(eventAtLimit)); err != nil {
			t.Fatalf("event at limit: %v", err)
		}
		assertFrameBytes(t, mustReceive(t, server), eventAtLimit)
		eventTooLarge := bytes.Repeat([]byte{'y'}, protocolv1.EventFrameMaxBytes+1)
		assertErrorIs(t, node.Send(context.Background(), transportpkg.NewFrame(eventTooLarge)), transportpkg.ErrFrameTooLarge)
	})

	t.Run("explicit backpressure without retry", func(t *testing.T) {
		server, node := newPair(t, factory, 1)
		defer closePair(server, node)

		first := transportpkg.NewFrame([]byte("first"))
		second := transportpkg.NewFrame([]byte("backpressure-canary"))
		if err := server.Send(context.Background(), first); err != nil {
			t.Fatal(err)
		}
		err := server.Send(context.Background(), second)
		assertErrorIs(t, err, transportpkg.ErrBackpressure)
		assertErrorSanitized(t, err, "backpressure-canary")
		assertFrameBytes(t, mustReceive(t, node), []byte("first"))
		assertReceiveDeadline(t, node)
		if err := server.Send(context.Background(), second); err != nil {
			t.Fatal(err)
		}
		assertFrameBytes(t, mustReceive(t, node), []byte("backpressure-canary"))
	})

	t.Run("context cancellation", func(t *testing.T) {
		server, node := newPair(t, factory, 2)
		defer closePair(server, node)

		canceled, cancel := context.WithCancel(context.Background())
		cancel()
		assertErrorIs(t, server.Send(canceled, transportpkg.NewFrame([]byte("not-sent"))), context.Canceled)
		_, err := node.Receive(canceled)
		assertErrorIs(t, err, context.Canceled)
		assertReceiveDeadline(t, node)
	})

	t.Run("peer close drains accepted frames", func(t *testing.T) {
		server, node := newPair(t, factory, 2)
		defer node.Close()
		if err := server.Send(context.Background(), transportpkg.NewFrame([]byte("one"))); err != nil {
			t.Fatal(err)
		}
		if err := server.Send(context.Background(), transportpkg.NewFrame([]byte("two"))); err != nil {
			t.Fatal(err)
		}
		if err := server.Close(); err != nil {
			t.Fatal(err)
		}
		if err := server.Close(); err != nil {
			t.Fatalf("second Close: %v", err)
		}
		assertFrameBytes(t, mustReceive(t, node), []byte("one"))
		assertFrameBytes(t, mustReceive(t, node), []byte("two"))
		_, err := node.Receive(context.Background())
		assertErrorIs(t, err, transportpkg.ErrClosed)
		assertErrorIs(t, node.Send(context.Background(), transportpkg.NewFrame([]byte("peer-closed"))), transportpkg.ErrClosed)
	})

	t.Run("local close stops both directions", func(t *testing.T) {
		server, node := newPair(t, factory, 2)
		defer server.Close()
		if err := node.Close(); err != nil {
			t.Fatal(err)
		}
		if err := node.Close(); err != nil {
			t.Fatalf("second Close: %v", err)
		}
		assertErrorIs(t, node.Send(context.Background(), transportpkg.NewFrame([]byte("closed"))), transportpkg.ErrClosed)
		_, err := node.Receive(context.Background())
		assertErrorIs(t, err, transportpkg.ErrClosed)
		_, err = server.Receive(context.Background())
		assertErrorIs(t, err, transportpkg.ErrClosed)
		assertErrorIs(t, server.Send(context.Background(), transportpkg.NewFrame([]byte("peer-closed"))), transportpkg.ErrClosed)
	})

	t.Run("blocked receive unblocks on close", func(t *testing.T) {
		server, node := newPair(t, factory, 2)
		defer node.Close()
		started := make(chan struct{})
		result := make(chan error, 1)
		go func() {
			close(started)
			_, err := node.Receive(context.Background())
			result <- err
		}()
		<-started
		if err := server.Close(); err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-result:
			assertErrorIs(t, err, transportpkg.ErrClosed)
		case <-time.After(time.Second):
			t.Fatal("Receive did not unblock after peer close")
		}
	})
}

func TestStandaloneOptions(t *testing.T) {
	server, node, err := transportpkg.NewStandalonePair(transportpkg.StandaloneOptions{})
	if err != nil {
		t.Fatal(err)
	}
	closePair(server, node)
	if _, _, err := transportpkg.NewStandalonePair(transportpkg.StandaloneOptions{QueueCapacity: -1}); err == nil {
		t.Fatal("negative queue capacity was accepted")
	}
}

func newPair(t *testing.T, factory pairFactory, capacity int) (transportpkg.Transport, transportpkg.Transport) {
	t.Helper()
	server, node, err := factory(capacity)
	if err != nil {
		t.Fatal(err)
	}
	return server, node
}

func closePair(server, node transportpkg.Transport) {
	_ = server.Close()
	_ = node.Close()
}

func mustReceive(t *testing.T, endpoint transportpkg.Transport) transportpkg.Frame {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	frame, err := endpoint.Receive(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return frame
}

func assertFrameBytes(t *testing.T, frame transportpkg.Frame, expected []byte) {
	t.Helper()
	if actual := frame.Bytes(); !bytes.Equal(actual, expected) {
		t.Fatalf("frame bytes differ: got %q, want %q", actual, expected)
	}
	if frame.Len() != len(expected) {
		t.Fatalf("frame length = %d, want %d", frame.Len(), len(expected))
	}
}

func assertReceiveDeadline(t *testing.T, endpoint transportpkg.Transport) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	_, err := endpoint.Receive(ctx)
	assertErrorIs(t, err, context.DeadlineExceeded)
}

func assertErrorIs(t *testing.T, err, expected error) {
	t.Helper()
	if !errors.Is(err, expected) {
		t.Fatalf("error = %v, want errors.Is(%v)", err, expected)
	}
}

func assertErrorSanitized(t *testing.T, err error, canary string) {
	t.Helper()
	if err != nil && strings.Contains(err.Error(), canary) {
		t.Fatal("transport error exposed frame contents")
	}
}

// relayFakeDirection and relayFakeEndpoint intentionally do not reuse the
// Standalone implementation. They model an opaque remote hop that copies each
// accepted frame while preserving the same single-session contract.
type relayFakeDirection struct {
	mu             sync.Mutex
	queue          chan transportpkg.Frame
	senderClosed   bool
	receiverClosed bool
}

func newRelayFakePair(capacity int) (transportpkg.Transport, transportpkg.Transport, error) {
	if capacity < 0 {
		return nil, nil, errors.New("relay fake capacity cannot be negative")
	}
	if capacity == 0 {
		capacity = transportpkg.DefaultQueueCapacity
	}
	serverToNode := &relayFakeDirection{queue: make(chan transportpkg.Frame, capacity)}
	nodeToServer := &relayFakeDirection{queue: make(chan transportpkg.Frame, capacity)}
	server := newRelayFakeEndpoint(nodeToServer, serverToNode, protocolv1.ControlFrameMaxBytes)
	node := newRelayFakeEndpoint(serverToNode, nodeToServer, protocolv1.EventFrameMaxBytes)
	return server, node, nil
}

func (d *relayFakeDirection) send(frame transportpkg.Frame) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.senderClosed || d.receiverClosed {
		return transportpkg.ErrClosed
	}
	remoteCopy := transportpkg.NewFrame(frame.Bytes())
	select {
	case d.queue <- remoteCopy:
		return nil
	default:
		return transportpkg.ErrBackpressure
	}
}

func (d *relayFakeDirection) closeSender() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.senderClosed {
		d.senderClosed = true
		close(d.queue)
	}
}

func (d *relayFakeDirection) closeReceiver() {
	d.mu.Lock()
	d.receiverClosed = true
	d.mu.Unlock()
}

type relayFakeEndpoint struct {
	in      *relayFakeDirection
	out     *relayFakeDirection
	maxSend int
	done    chan struct{}
	once    sync.Once
}

func newRelayFakeEndpoint(in, out *relayFakeDirection, maxSend int) *relayFakeEndpoint {
	return &relayFakeEndpoint{in: in, out: out, maxSend: maxSend, done: make(chan struct{})}
}

func (e *relayFakeEndpoint) Send(ctx context.Context, frame transportpkg.Frame) error {
	if ctx == nil {
		return context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-e.done:
		return transportpkg.ErrClosed
	default:
	}
	if frame.Len() > e.maxSend {
		return transportpkg.ErrFrameTooLarge
	}
	return e.out.send(frame)
}

func (e *relayFakeEndpoint) Receive(ctx context.Context) (transportpkg.Frame, error) {
	if ctx == nil {
		return transportpkg.Frame{}, context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return transportpkg.Frame{}, err
	}
	select {
	case <-e.done:
		return transportpkg.Frame{}, transportpkg.ErrClosed
	default:
	}
	select {
	case <-ctx.Done():
		return transportpkg.Frame{}, ctx.Err()
	case <-e.done:
		return transportpkg.Frame{}, transportpkg.ErrClosed
	case frame, ok := <-e.in.queue:
		if !ok {
			return transportpkg.Frame{}, transportpkg.ErrClosed
		}
		return frame, nil
	}
}

func (e *relayFakeEndpoint) Close() error {
	e.once.Do(func() {
		close(e.done)
		e.out.closeSender()
		e.in.closeReceiver()
	})
	return nil
}
