//go:build darwin

package platform

const (
	expectedCurrentFamily      = FamilyDarwin
	expectedProcessAvailable   = false
	expectedWorkspaceAvailable = false
	expectedIPCAvailable       = false
)
