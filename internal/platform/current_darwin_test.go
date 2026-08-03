//go:build darwin

package platform

const (
	expectedCurrentFamily      = FamilyDarwin
	expectedAutostartAvailable = true
	expectedProcessAvailable   = true
	expectedWorkspaceAvailable = true
	expectedIPCAvailable       = true
)
