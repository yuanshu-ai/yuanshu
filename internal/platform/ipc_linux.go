//go:build linux

package platform

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"
)

type linuxLocalIPC struct{}

func newLinuxLocalIPC() LocalIPC      { return linuxLocalIPC{} }
func (linuxLocalIPC) Available() bool { return true }

func (linuxLocalIPC) Listen(ctx context.Context, name IPCName) (net.Listener, error) {
	if err := linuxIPCContext(ctx); err != nil {
		return nil, err
	}
	path, err := linuxSocketPath(name)
	if err != nil {
		return nil, err
	}
	if _, err := os.Lstat(path); err == nil {
		connection, dialErr := net.DialTimeout("unix", path, 100*time.Millisecond)
		if dialErr == nil {
			_ = connection.Close()
			return nil, ErrAlreadyExists
		}
		if os.Remove(path) != nil {
			return nil, ErrAlreadyExists
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, ErrUnavailable
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, ErrUnavailable
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = listener.Close()
		_ = os.Remove(path)
		return nil, ErrUnavailable
	}
	return &linuxIPCListener{Listener: listener, path: path}, nil
}

func (linuxLocalIPC) Dial(ctx context.Context, name IPCName) (net.Conn, error) {
	if err := linuxIPCContext(ctx); err != nil {
		return nil, err
	}
	path, err := linuxSocketPath(name)
	if err != nil {
		return nil, err
	}
	dialer := net.Dialer{}
	connection, err := dialer.DialContext(ctx, "unix", path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED) {
			return nil, ErrNotFound
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ErrUnavailable
	}
	return connection, nil
}

type linuxIPCListener struct {
	net.Listener
	path string
	once sync.Once
	err  error
}

func (l *linuxIPCListener) Close() error {
	l.once.Do(func() {
		l.err = l.Listener.Close()
		removeErr := os.Remove(l.path)
		if errors.Is(removeErr, os.ErrNotExist) {
			removeErr = nil
		}
		if l.err == nil {
			l.err = removeErr
		}
	})
	return l.err
}

func linuxSocketPath(name IPCName) (string, error) {
	if !validLinuxIPCName(string(name)) {
		return "", ErrInvalidArgument
	}
	base := os.Getenv("XDG_RUNTIME_DIR")
	if !filepath.IsAbs(base) {
		base = os.TempDir()
	}
	directory := filepath.Join(base, fmt.Sprintf("yuanshu-%d", os.Getuid()))
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", ErrUnavailable
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
		return "", ErrUnavailable
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Getuid()) {
		return "", ErrUnavailable
	}
	path := filepath.Join(directory, string(name)+".sock")
	if len(path) >= 100 {
		return "", ErrUnavailable
	}
	return path, nil
}

func validLinuxIPCName(value string) bool {
	if value == "" || len(value) > 64 || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if unicode.IsLetter(character) || unicode.IsDigit(character) || character == '-' || character == '_' || character == '.' {
			continue
		}
		return false
	}
	return true
}

func linuxIPCContext(ctx context.Context) error {
	if ctx == nil {
		return context.Canceled
	}
	return ctx.Err()
}
