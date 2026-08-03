//go:build linux

package platform

const (
	expectedCurrentFamily      = FamilyLinux
	expectedAutostartAvailable = false
	expectedProcessAvailable   = false
	expectedWorkspaceAvailable = false
	expectedIPCAvailable       = true
)
