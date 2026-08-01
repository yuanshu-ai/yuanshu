//go:build !windows

package config

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestSavedConfigurationIsPrivateOnUnix(t *testing.T) {
	path := filepath.Join(t.TempDir(), "node.toml")
	store, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(context.Background(), validConfig("permissions")); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 600", info.Mode().Perm())
	}
}
