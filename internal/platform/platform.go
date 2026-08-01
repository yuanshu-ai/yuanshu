// Package platform defines the operating-system capability boundary used by
// Yuanshu's platform-neutral core.
package platform

import (
	"context"
	"errors"
	"io"
	"net"
)

type Family string

const (
	FamilyWindows Family = "windows"
	FamilyDarwin  Family = "darwin"
	FamilyLinux   Family = "linux"
)

var (
	ErrUnavailable     = errors.New("platform capability is unavailable")
	ErrNotFound        = errors.New("platform resource was not found")
	ErrAlreadyExists   = errors.New("platform resource already exists")
	ErrInvalidArgument = errors.New("platform argument is invalid")
	ErrProcessStopped  = errors.New("platform process is stopped")
)

type Platform interface {
	Family() Family
	SecureStore() SecureStore
	Processes() ProcessManager
	IPC() LocalIPC
	Autostart() AutostartManager
	Workspaces() WorkspaceInspector
}

// SecretRef is an opaque identifier. Its representation is owned by the
// selected SecureStore implementation and must not be persisted as a secret.
type SecretRef string

type SecureStore interface {
	Available() bool
	Put(ctx context.Context, ref SecretRef, secret []byte) error
	Get(ctx context.Context, ref SecretRef) ([]byte, error)
	Delete(ctx context.Context, ref SecretRef) error
}

// ProcessSpec describes a direct executable launch. Env nil means inherit the
// current environment; a non-nil Env is the complete replacement environment.
type ProcessSpec struct {
	Executable string
	Args       []string
	Env        []string
	Directory  string
}

type ProcessExit struct {
	Code   int
	Forced bool
}

type Process interface {
	Stdin() io.WriteCloser
	Stdout() io.ReadCloser
	Stderr() io.ReadCloser
	Wait(ctx context.Context) (ProcessExit, error)
	Stop(ctx context.Context) (ProcessExit, error)
}

type ProcessManager interface {
	Available() bool
	Start(ctx context.Context, spec ProcessSpec) (Process, error)
}

// IPCName is a logical endpoint identifier. Only a LocalIPC implementation
// may translate it into an operating-system pipe or socket address.
type IPCName string

type LocalIPC interface {
	Available() bool
	Listen(ctx context.Context, name IPCName) (net.Listener, error)
	Dial(ctx context.Context, name IPCName) (net.Conn, error)
}

type AutostartEntry struct {
	ID         string
	Executable string
	Args       []string
	Env        []string
	Directory  string
}

type AutostartStatus struct {
	Installed bool
	Entry     AutostartEntry
}

type AutostartManager interface {
	Available() bool
	Install(ctx context.Context, entry AutostartEntry) error
	Remove(ctx context.Context, id string) error
	Status(ctx context.Context, id string) (AutostartStatus, error)
}

// WorkspaceFacts contains OS observations only. Product allow/deny policy is
// deliberately owned by the Node workspace policy layer, not Platform.
type WorkspaceFacts struct {
	CanonicalPath          string
	FilesystemRoot         string
	IsFilesystemRoot       bool
	IsHome                 bool
	IsSystem               bool
	CrossesLinkBoundary    bool
	CrossesReparseBoundary bool
}

type WorkspaceInspector interface {
	Available() bool
	Inspect(ctx context.Context, path string) (WorkspaceFacts, error)
}
