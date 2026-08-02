//go:build linux

package platform

import (
	"errors"
	"io"
	"path/filepath"
)

type LinuxStandaloneOptions struct {
	DataDir       string
	MasterKeyFile string
}

type linuxStandalonePlatform struct {
	secure     *linuxEncryptedStore
	processes  ProcessManager
	ipc        LocalIPC
	workspaces WorkspaceInspector
}

func NewLinuxStandalone(options LinuxStandaloneOptions) (Platform, io.Closer, error) {
	if !filepath.IsAbs(options.DataDir) || !filepath.IsAbs(options.MasterKeyFile) {
		return nil, nil, ErrInvalidArgument
	}
	secure, err := newLinuxEncryptedStore(filepath.Join(options.DataDir, "node", "secrets"), options.MasterKeyFile)
	if err != nil {
		return nil, nil, err
	}
	value := &linuxStandalonePlatform{secure: secure, processes: newLinuxProcessManager(), ipc: newLinuxLocalIPC(), workspaces: newLinuxWorkspaceInspector()}
	return value, value, nil
}

func (*linuxStandalonePlatform) Family() Family                   { return FamilyLinux }
func (p *linuxStandalonePlatform) SecureStore() SecureStore       { return p.secure }
func (p *linuxStandalonePlatform) Processes() ProcessManager      { return p.processes }
func (p *linuxStandalonePlatform) IPC() LocalIPC                  { return p.ipc }
func (p *linuxStandalonePlatform) Workspaces() WorkspaceInspector { return p.workspaces }
func (*linuxStandalonePlatform) Autostart() AutostartManager      { return unavailableCapabilities{} }

func (p *linuxStandalonePlatform) Close() error {
	if p.secure == nil {
		return errors.New("platform secret store is unavailable")
	}
	return p.secure.Close()
}
