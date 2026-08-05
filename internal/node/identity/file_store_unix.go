//go:build !windows

package identity

import (
	"os"
	"syscall"
)

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
