//go:build windows

package identity

import (
	"os"

	"golang.org/x/sys/windows"
)

// Windows Node data lives below LocalAppData, whose default ACL is scoped to
// the current user. Keep the requested restrictive mode as an additional
// invariant; Windows enforces the ACL rather than Unix mode bits.
func infoPrivateDirectory(_ string, _ os.FileInfo) error   { return nil }
func infoPrivateKey(_ os.FileInfo) error                   { return nil }
func trustedDirectorySymlink(_ string, _ os.FileInfo) bool { return false }
func replaceIdentityFile(from, to string) error            { return windows.Rename(from, to) }
func syncPrivateDirectory(string) error                    { return nil }
