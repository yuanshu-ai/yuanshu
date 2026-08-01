//go:build windows

package platform

import (
	"path/filepath"

	"golang.org/x/sys/windows"
)

type windowsPlatform struct {
	secure     SecureStore
	workspaces WorkspaceInspector
	all        unavailableCapabilities
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
		workspaces: newWindowsWorkspaceInspector(),
	}
}

func (*windowsPlatform) Family() Family                   { return FamilyWindows }
func (p *windowsPlatform) SecureStore() SecureStore       { return p.secure }
func (p *windowsPlatform) Processes() ProcessManager      { return p.all }
func (p *windowsPlatform) IPC() LocalIPC                  { return p.all }
func (p *windowsPlatform) Autostart() AutostartManager    { return p.all }
func (p *windowsPlatform) Workspaces() WorkspaceInspector { return p.workspaces }
