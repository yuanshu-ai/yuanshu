package identity

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestFileKeyStoreCreatesPrivateKeyAtomically(t *testing.T) {
	root := filepath.Join(t.TempDir(), "node")
	path := filepath.Join(root, "identity.key")
	keys, err := NewFileKeyStore(path)
	if err != nil {
		t.Fatal(err)
	}
	seed := bytes.Repeat([]byte{0x41}, ed25519.SeedSize)
	if err := keys.Put(context.Background(), seed); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("key mode = %o, want 600", info.Mode().Perm())
	}
	rootInfo, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	if rootInfo.Mode().Perm() != 0o700 {
		t.Fatalf("root mode = %o, want 700", rootInfo.Mode().Perm())
	}
	got, err := keys.Get(context.Background())
	if err != nil || !bytes.Equal(got, seed) {
		t.Fatalf("Get() = %x, %v", got, err)
	}
	clear(got)
}

func TestFileKeyStoreRejectsUnsafeAndCorruptFiles(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "identity.key")
	keys, err := NewFileKeyStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := keys.Get(context.Background()); !errors.Is(err, ErrInvalid) {
		t.Fatalf("corrupt key error = %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "other"), path); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := keys.Get(context.Background()); !errors.Is(err, ErrInvalid) {
		t.Fatalf("symlink error = %v", err)
	}
}

func TestFileKeyStoreRejectsSymlinkedParent(t *testing.T) {
	root := t.TempDir()
	actual := filepath.Join(root, "actual")
	if err := os.Mkdir(actual, 0o700); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(root, "linked")
	if err := os.Symlink(actual, linked); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	keys, err := NewFileKeyStore(filepath.Join(linked, "identity.key"))
	if err != nil {
		t.Fatal(err)
	}
	if err := keys.Put(context.Background(), bytes.Repeat([]byte{0x22}, ed25519.SeedSize)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("symlinked parent Put() = %v", err)
	}
}

func TestFileKeyStoreRejectsWrongBasename(t *testing.T) {
	if _, err := NewFileKeyStore(filepath.Join(t.TempDir(), "wrong.key")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("wrong basename error = %v", err)
	}
}
