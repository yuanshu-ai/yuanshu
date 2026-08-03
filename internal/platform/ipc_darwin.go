//go:build darwin

package platform

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"
)

type darwinLocalIPC struct{}

func newDarwinLocalIPC() LocalIPC      { return darwinLocalIPC{} }
func (darwinLocalIPC) Available() bool { return true }

func (darwinLocalIPC) Listen(ctx context.Context, name IPCName) (net.Listener, error) {
	if err := darwinIPCContext(ctx); err != nil {
		return nil, err
	}
	path, err := darwinSocketPath(name)
	if err != nil {
		return nil, err
	}
	if _, err := os.Lstat(path); err == nil {
		connection, dialErr := net.DialTimeout("unix", path, 100*time.Millisecond)
		if dialErr == nil {
			_ = connection.Close()
			return nil, ErrAlreadyExists
		}
		if removeErr := os.Remove(path); removeErr != nil {
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
	return &darwinIPCListener{Listener: listener, path: path}, nil
}

func (darwinLocalIPC) Dial(ctx context.Context, name IPCName) (net.Conn, error) {
	if err := darwinIPCContext(ctx); err != nil {
		return nil, err
	}
	path, err := darwinSocketPath(name)
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

type darwinIPCListener struct {
	net.Listener
	path string
	once sync.Once
	err  error
}

func (l *darwinIPCListener) Close() error {
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

func darwinSocketPath(name IPCName) (string, error) {
	if !validDarwinIPCName(string(name)) {
		return "", ErrInvalidArgument
	}
	home, err := os.UserHomeDir()
	if err != nil || !filepath.IsAbs(home) {
		return "", ErrUnavailable
	}
	directory := filepath.Join(home, "Library", "Application Support", "Yuanshu", "run")
	if err := ensureDarwinPrivateDirectory(directory); err != nil {
		return "", err
	}
	path := filepath.Join(directory, string(name)+".sock")
	if len(path) >= 100 {
		return "", ErrUnavailable
	}
	return path, nil
}

func ensureDarwinPrivateDirectory(path string) error {
	if info, err := os.Lstat(path); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return ErrUnavailable
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return ErrUnavailable
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return ErrUnavailable
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
		return ErrUnavailable
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Getuid()) {
		return ErrUnavailable
	}
	return nil
}

func validDarwinIPCName(value string) bool {
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

func darwinIPCContext(ctx context.Context) error {
	if ctx == nil {
		return context.Canceled
	}
	return ctx.Err()
}
