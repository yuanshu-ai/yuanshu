// Package fake provides stateful, concurrency-safe Platform capabilities for
// unit and contract tests. It never reads or modifies operating-system state.
package fake

import (
	"context"
	"sync"

	platformpkg "github.com/yuanshu-ai/yuanshu/internal/platform"
)

type injectedFailure struct {
	mu  sync.RWMutex
	err error
}

func (f *injectedFailure) set(err error) {
	f.mu.Lock()
	f.err = err
	f.mu.Unlock()
}

func (f *injectedFailure) get(ctx context.Context) error {
	if ctx == nil {
		return context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.err
}

type Platform struct {
	family     platformpkg.Family
	secrets    *SecureStore
	processes  *ProcessManager
	ipc        *LocalIPC
	autostart  *AutostartManager
	workspaces *WorkspaceInspector
	picker     *DirectoryPicker
}

var _ platformpkg.Platform = (*Platform)(nil)

func New(family platformpkg.Family) (*Platform, error) {
	switch family {
	case platformpkg.FamilyWindows, platformpkg.FamilyDarwin, platformpkg.FamilyLinux:
	default:
		return nil, platformpkg.ErrInvalidArgument
	}
	return &Platform{
		family:     family,
		secrets:    NewSecureStore(),
		processes:  NewProcessManager(),
		ipc:        NewLocalIPC(),
		autostart:  NewAutostartManager(),
		workspaces: NewWorkspaceInspector(),
		picker:     NewDirectoryPicker(),
	}, nil
}

func (p *Platform) Family() platformpkg.Family                   { return p.family }
func (p *Platform) SecureStore() platformpkg.SecureStore         { return p.secrets }
func (p *Platform) Processes() platformpkg.ProcessManager        { return p.processes }
func (p *Platform) IPC() platformpkg.LocalIPC                    { return p.ipc }
func (p *Platform) Autostart() platformpkg.AutostartManager      { return p.autostart }
func (p *Platform) Workspaces() platformpkg.WorkspaceInspector   { return p.workspaces }
func (p *Platform) DirectoryPicker() platformpkg.DirectoryPicker { return p.picker }
func (p *Platform) FakeSecureStore() *SecureStore                { return p.secrets }
func (p *Platform) FakeProcesses() *ProcessManager               { return p.processes }
func (p *Platform) FakeIPC() *LocalIPC                           { return p.ipc }
func (p *Platform) FakeAutostart() *AutostartManager             { return p.autostart }
func (p *Platform) FakeWorkspaces() *WorkspaceInspector          { return p.workspaces }
func (p *Platform) FakeDirectoryPicker() *DirectoryPicker        { return p.picker }
