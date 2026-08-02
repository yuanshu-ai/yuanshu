//go:build !windows

package server

import (
	"errors"
	"os"
)

func validatePrivateKeyPermissions(path string) error {
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm()&0o077 != 0 {
		return errors.New("server TLS private key permissions are unsafe")
	}
	return nil
}
