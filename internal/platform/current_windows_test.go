//go:build windows

package platform

const (
	expectedCurrentFamily        = FamilyWindows
	expectedSecureStoreAvailable = true
	expectedAutostartAvailable   = true
	expectedProcessAvailable     = true
	expectedWorkspaceAvailable   = true
	expectedIPCAvailable         = true
)
