//go:build !windows

package server

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTLSPrivateKeyRejectsGroupOrOtherPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.key")
	if err := os.WriteFile(path, []byte("synthetic"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := validatePrivateKeyPermissions(path); err == nil {
		t.Fatal("unsafe private-key permissions were accepted")
	}
}
