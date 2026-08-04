package platform

import (
	"context"
	"net"
)

type unavailablePlatform struct {
	family Family
	all    unavailableCapabilities
}

func newUnavailablePlatform(family Family) Platform {
	return &unavailablePlatform{family: family}
}

func (p *unavailablePlatform) Family() Family                   { return p.family }
func (p *unavailablePlatform) SecureStore() SecureStore         { return p.all }
func (p *unavailablePlatform) Processes() ProcessManager        { return p.all }
func (p *unavailablePlatform) IPC() LocalIPC                    { return p.all }
func (p *unavailablePlatform) Autostart() AutostartManager      { return p.all }
func (p *unavailablePlatform) Workspaces() WorkspaceInspector   { return p.all }
func (p *unavailablePlatform) DirectoryPicker() DirectoryPicker { return p.all }

type unavailableCapabilities struct{}

func (unavailableCapabilities) Available() bool { return false }

func (unavailableCapabilities) Put(context.Context, SecretRef, []byte) error {
	return ErrUnavailable
}

func (unavailableCapabilities) Get(context.Context, SecretRef) ([]byte, error) {
	return nil, ErrUnavailable
}

func (unavailableCapabilities) Delete(context.Context, SecretRef) error {
	return ErrUnavailable
}

func (unavailableCapabilities) Start(context.Context, ProcessSpec) (Process, error) {
	return nil, ErrUnavailable
}

func (unavailableCapabilities) Listen(context.Context, IPCName) (net.Listener, error) {
	return nil, ErrUnavailable
}

func (unavailableCapabilities) Dial(context.Context, IPCName) (net.Conn, error) {
	return nil, ErrUnavailable
}

func (unavailableCapabilities) Install(context.Context, AutostartEntry) error {
	return ErrUnavailable
}

func (unavailableCapabilities) Remove(context.Context, string) error {
	return ErrUnavailable
}

func (unavailableCapabilities) Status(context.Context, string) (AutostartStatus, error) {
	return AutostartStatus{}, ErrUnavailable
}

func (unavailableCapabilities) Inspect(context.Context, string) (WorkspaceFacts, error) {
	return WorkspaceFacts{}, ErrUnavailable
}

func (unavailableCapabilities) PickDirectory(context.Context) (DirectorySelection, error) {
	return DirectorySelection{}, ErrUnavailable
}
