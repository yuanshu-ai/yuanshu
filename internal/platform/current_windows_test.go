//go:build windows

package platform

const (
	expectedCurrentFamily      = FamilyWindows
	expectedProcessAvailable   = true
	expectedWorkspaceAvailable = true
	expectedIPCAvailable       = true
)
