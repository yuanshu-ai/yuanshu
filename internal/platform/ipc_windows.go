//go:build windows

package platform

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net"
	"strings"
	"unicode"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

type windowsLocalIPC struct{}

func newWindowsLocalIPC() LocalIPC { return windowsLocalIPC{} }

func (windowsLocalIPC) Available() bool { return true }

func (windowsLocalIPC) Listen(ctx context.Context, name IPCName) (net.Listener, error) {
	if err := platformContext(ctx); err != nil {
		return nil, err
	}
	path, descriptor, err := windowsPipeIdentity(name)
	if err != nil {
		return nil, err
	}
	// go-winio v0.6.2 creates every server handle with
	// FILE_PIPE_REJECT_REMOTE_CLIENTS; the DACL below adds the current-user
	// identity boundary on top of that transport-level restriction.
	listener, err := winio.ListenPipe(path, &winio.PipeConfig{
		SecurityDescriptor: descriptor,
		InputBufferSize:    64 << 10,
		OutputBufferSize:   64 << 10,
	})
	if err != nil {
		if errors.Is(err, windows.ERROR_ACCESS_DENIED) || errors.Is(err, windows.ERROR_PIPE_BUSY) {
			return nil, ErrAlreadyExists
		}
		return nil, ErrUnavailable
	}
	return listener, nil
}

func (windowsLocalIPC) Dial(ctx context.Context, name IPCName) (net.Conn, error) {
	if err := platformContext(ctx); err != nil {
		return nil, err
	}
	path, _, err := windowsPipeIdentity(name)
	if err != nil {
		return nil, err
	}
	connection, err := winio.DialPipeContext(ctx, path)
	if err != nil {
		if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) || errors.Is(err, windows.ERROR_PIPE_BUSY) {
			return nil, ErrNotFound
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ErrUnavailable
	}
	return connection, nil
}

func windowsPipeIdentity(name IPCName) (string, string, error) {
	if !validIPCName(string(name)) {
		return "", "", ErrInvalidArgument
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil || user == nil || user.User.Sid == nil {
		return "", "", ErrUnavailable
	}
	sid := user.User.Sid.String()
	digest := sha256.Sum256([]byte(sid))
	path := fmt.Sprintf(`\\.\pipe\yuanshu-%x-%s`, digest[:8], name)
	descriptor := fmt.Sprintf("D:P(A;;GA;;;SY)(A;;GA;;;%s)", sid)
	return path, descriptor, nil
}

func validIPCName(value string) bool {
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

func platformContext(ctx context.Context) error {
	if ctx == nil {
		return context.Canceled
	}
	return ctx.Err()
}
