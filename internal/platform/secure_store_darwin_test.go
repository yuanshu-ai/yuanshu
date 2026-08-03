//go:build darwin && cgo

package platform

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestDarwinKeychainRoundTripReplaceAndDelete(t *testing.T) {
	store := newDarwinKeychain()
	ref := SecretRef("test/" + strings.ToLower(strings.ReplaceAll(t.Name(), "/", "-")))
	defer func() { _ = store.Delete(context.Background(), ref) }()

	secret := []byte("darwin-keychain-secret")
	if err := store.Put(context.Background(), ref, secret); err != nil {
		t.Fatal(err)
	}
	secret[0] = 'X'
	got, err := store.Get(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "darwin-keychain-secret" {
		t.Fatalf("Get() = %q", got)
	}
	clear(got)

	if err := store.Put(context.Background(), ref, []byte("replacement")); err != nil {
		t.Fatal(err)
	}
	got, err = store.Get(context.Background(), ref)
	if err != nil || string(got) != "replacement" {
		t.Fatalf("replacement Get() = %q, %v", got, err)
	}
	clear(got)

	if err := store.Delete(context.Background(), ref); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(context.Background(), ref); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get() after Delete() = %v, want ErrNotFound", err)
	}
	if err := store.Delete(context.Background(), ref); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second Delete() = %v, want ErrNotFound", err)
	}
}

func TestDarwinKeychainDoesNotExposeRefOrSecretInErrors(t *testing.T) {
	store := newDarwinKeychain()
	const canary = "keychain-error-canary"
	if err := store.Put(context.Background(), SecretRef("bad\n"+canary), []byte(canary)); !errors.Is(err, ErrInvalidArgument) || strings.Contains(err.Error(), canary) {
		t.Fatalf("Put() error = %v", err)
	}
	if _, err := store.Get(context.Background(), SecretRef("missing/"+canary)); !errors.Is(err, ErrNotFound) || strings.Contains(err.Error(), canary) {
		t.Fatalf("Get() error = %v", err)
	}
}
