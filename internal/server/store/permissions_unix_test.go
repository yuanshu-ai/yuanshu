//go:build !windows

package store

import (
	"os"
	"testing"
)

func TestOpenAppliesPrivateFilePermissions(t *testing.T) {
	local, path := openTestStore(t)
	if err := local.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("database permissions=%#o, want 0600", got)
	}
}
