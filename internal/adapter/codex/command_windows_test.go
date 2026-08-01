//go:build windows

package codex

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveConfiguredCommandUsesStandardNPMLayoutWithoutShell(t *testing.T) {
	root := t.TempDir()
	shim := filepath.Join(root, "codex.cmd")
	script := filepath.Join(root, "node_modules", "@openai", "codex", "bin", "codex.js")
	if err := os.MkdirAll(filepath.Dir(script), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(shim, []byte("synthetic shim that must not execute"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(script, []byte("synthetic script"), 0o600); err != nil {
		t.Fatal(err)
	}
	executable, prefix, err := resolveConfiguredCommand(shim)
	if err != nil {
		t.Fatal(err)
	}
	if executable != "node" || len(prefix) != 1 || prefix[0] != script {
		t.Fatalf("resolved command = %q %#v", executable, prefix)
	}
	if executable == shim {
		t.Fatal("resolver returned the command shim for execution")
	}
}

func TestResolveConfiguredCommandRejectsUnknownShim(t *testing.T) {
	if _, _, err := resolveConfiguredCommand(`C:\synthetic\other.cmd`); err == nil {
		t.Fatal("unknown command shim was accepted")
	}
}
