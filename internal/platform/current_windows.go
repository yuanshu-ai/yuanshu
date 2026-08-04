//go:build windows

package platform

import (
	"path/filepath"

	"golang.org/x/sys/windows"
)

type windowsPlatform struct {
	secure     SecureStore
	processes  ProcessManager
	ipc        LocalIPC
	autostart  AutostartManager
	workspaces WorkspaceInspector
	picker     DirectoryPicker
}

func Current() Platform {
	return &windowsPlatform{
		secure: newDPAPISecureStore(func() (string, error) {
			root, err := windows.KnownFolderPath(windows.FOLDERID_LocalAppData, windows.KF_FLAG_DEFAULT)
			if err != nil {
				return "", ErrUnavailable
			}
			return filepath.Join(root, "Yuanshu", "secrets-v1"), nil
		}),
		processes:  newWindowsProcessManager(),
		ipc:        newWindowsLocalIPC(),
		autostart:  newWindowsAutostartManager(),
		workspaces: newWindowsWorkspaceInspector(),
		picker:     newWindowsDirectoryPicker(),
	}
}

func (*windowsPlatform) Family() Family                     { return FamilyWindows }
func (p *windowsPlatform) SecureStore() SecureStore         { return p.secure }
func (p *windowsPlatform) Processes() ProcessManager        { return p.processes }
func (p *windowsPlatform) IPC() LocalIPC                    { return p.ipc }
func (p *windowsPlatform) Autostart() AutostartManager      { return p.autostart }
func (p *windowsPlatform) Workspaces() WorkspaceInspector   { return p.workspaces }
func (p *windowsPlatform) DirectoryPicker() DirectoryPicker { return p.picker }
