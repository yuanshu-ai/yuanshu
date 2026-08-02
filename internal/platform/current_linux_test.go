//go:build linux

package platform

const (
	expectedCurrentFamily      = FamilyLinux
	expectedProcessAvailable   = false
	expectedWorkspaceAvailable = false
	expectedIPCAvailable       = true
)
