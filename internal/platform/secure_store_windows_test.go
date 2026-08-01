//go:build windows

package platform

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDPAPISecureStoreRoundTripAndDiskRedaction(t *testing.T) {
	root := t.TempDir()
	store := newDPAPISecureStore(func() (string, error) { return root, nil })
	ref := SecretRef("test/identity/canary-ref")
	secret := []byte("dpapi-plaintext-canary")

	if err := store.Put(context.Background(), ref, secret); err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	secret[0] = 'X'
	got, err := store.Get(context.Background(), ref)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if string(got) != "dpapi-plaintext-canary" {
		t.Fatal("Get() returned the wrong secret")
	}

	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 1 {
		t.Fatalf("ReadDir() = %v, %v", entries, err)
	}
	if strings.Contains(entries[0].Name(), string(ref)) {
		t.Fatal("secret reference was exposed in the file name")
	}
	raw, err := os.ReadFile(filepath.Join(root, entries[0].Name()))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if bytes.Contains(raw, []byte("dpapi-plaintext-canary")) {
		t.Fatal("plaintext secret was written to disk")
	}

	if err := store.Put(context.Background(), ref, []byte("replacement")); err != nil {
		t.Fatalf("replacement Put() error = %v", err)
	}
	got, err = store.Get(context.Background(), ref)
	if err != nil || string(got) != "replacement" {
		t.Fatal("replacement secret was not returned")
	}
	clear(got)
	if err := store.Delete(context.Background(), ref); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if _, err := store.Get(context.Background(), ref); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get() after delete error = %v, want ErrNotFound", err)
	}
	if err := store.Delete(context.Background(), ref); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second Delete() error = %v, want ErrNotFound", err)
	}
}

func TestDPAPISecureStoreRejectsCorruptionWithoutCanary(t *testing.T) {
	root := t.TempDir()
	store := newDPAPISecureStore(func() (string, error) { return root, nil })
	ref := SecretRef("corrupt-ref-canary")
	_, path, err := store.resolve(ref)
	if err != nil {
		t.Fatalf("resolve() error = %v", err)
	}
	if err := os.WriteFile(path, []byte("not-a-secret"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	_, err = store.Get(context.Background(), ref)
	if err == nil || strings.Contains(err.Error(), string(ref)) || strings.Contains(err.Error(), path) {
		t.Fatalf("Get() returned an unsafe error")
	}
}
