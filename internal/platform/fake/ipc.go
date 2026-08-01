package fake

import (
	"context"
	"net"
	"sync"

	platformpkg "github.com/yuanshu-ai/yuanshu/internal/platform"
)

const ipcAcceptCapacity = 64

type LocalIPC struct {
	failure   injectedFailure
	mu        sync.Mutex
	listeners map[platformpkg.IPCName]*memoryListener
}

var _ platformpkg.LocalIPC = (*LocalIPC)(nil)

func NewLocalIPC() *LocalIPC {
	return &LocalIPC{listeners: make(map[platformpkg.IPCName]*memoryListener)}
}

func (*LocalIPC) Available() bool { return true }

func (i *LocalIPC) SetError(err error) { i.failure.set(err) }

func (i *LocalIPC) Listen(ctx context.Context, name platformpkg.IPCName) (net.Listener, error) {
	if err := i.failure.get(ctx); err != nil {
		return nil, err
	}
	if name == "" {
		return nil, platformpkg.ErrInvalidArgument
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if _, ok := i.listeners[name]; ok {
		return nil, platformpkg.ErrAlreadyExists
	}
	listener := &memoryListener{
		connections: make(chan net.Conn, ipcAcceptCapacity),
		done:        make(chan struct{}),
	}
	listener.onClose = func() {
		i.mu.Lock()
		if i.listeners[name] == listener {
			delete(i.listeners, name)
		}
		i.mu.Unlock()
	}
	i.listeners[name] = listener
	return listener, nil
}

func (i *LocalIPC) Dial(ctx context.Context, name platformpkg.IPCName) (net.Conn, error) {
	if err := i.failure.get(ctx); err != nil {
		return nil, err
	}
	if name == "" {
		return nil, platformpkg.ErrInvalidArgument
	}
	i.mu.Lock()
	listener := i.listeners[name]
	i.mu.Unlock()
	if listener == nil {
		return nil, platformpkg.ErrNotFound
	}
	client, server := net.Pipe()
	if err := listener.enqueue(ctx, server); err != nil {
		_ = client.Close()
		_ = server.Close()
		return nil, err
	}
	return client, nil
}

type memoryListener struct {
	mu          sync.Mutex
	connections chan net.Conn
	done        chan struct{}
	onClose     func()
	closeOnce   sync.Once
	closed      bool
}

func (l *memoryListener) enqueue(ctx context.Context, connection net.Conn) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return platformpkg.ErrNotFound
	}
	select {
	case l.connections <- connection:
		return nil
	default:
		return platformpkg.ErrUnavailable
	}
}

func (l *memoryListener) Accept() (net.Conn, error) {
	select {
	case <-l.done:
		return nil, net.ErrClosed
	default:
	}
	select {
	case <-l.done:
		return nil, net.ErrClosed
	case connection := <-l.connections:
		return connection, nil
	}
}

func (l *memoryListener) Close() error {
	l.closeOnce.Do(func() {
		l.mu.Lock()
		l.closed = true
		close(l.done)
		for {
			select {
			case connection := <-l.connections:
				_ = connection.Close()
			default:
				l.mu.Unlock()
				l.onClose()
				return
			}
		}
	})
	return nil
}

func (*memoryListener) Addr() net.Addr { return memoryAddress{} }

type memoryAddress struct{}

func (memoryAddress) Network() string { return "yuanshu-memory-ipc" }
func (memoryAddress) String() string  { return "memory-endpoint" }
