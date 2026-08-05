//go:build !windows

package identity

import (
	"os"
	"path/filepath"
	"runtime"
	"syscall"
)

// macOS exposes /tmp and /var as root-owned aliases into /private. They are
// part of the operating system's trusted path layout, not user-controlled
// workspace links. Other symlinked parents remain rejected.
func trustedDirectorySymlink(path string, info os.FileInfo) bool {
	if runtime.GOOS != "darwin" || (filepath.Clean(path) != "/tmp" && filepath.Clean(path) != "/var") {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && uint32(stat.Uid) == 0 && info.Mode().Perm()&0o022 == 0
}

func infoPrivateDirectory(path string, info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if ok && uint32(stat.Uid) != uint32(os.Getuid()) {
		return ErrInvalid
	}
	return nil
}

func infoPrivateKey(info os.FileInfo) error {
	if info.Mode().Perm()&0o077 != 0 {
		return ErrInvalid
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if ok && uint32(stat.Uid) != uint32(os.Getuid()) {
		return ErrInvalid
	}
	return nil
}

func replaceIdentityFile(from, to string) error { return os.Rename(from, to) }

func syncPrivateDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
