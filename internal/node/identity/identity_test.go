package identity

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yuanshu-ai/yuanshu/internal/node/store"
	"github.com/yuanshu-ai/yuanshu/internal/platform"
	platformfake "github.com/yuanshu-ai/yuanshu/internal/platform/fake"
)

func openIdentityStore(t *testing.T) *store.Store {
	t.Helper()
	local, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "node.db"), store.Options{Clock: func() time.Time { return fixedIdentityTime }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = local.Close() })
	return local
}

var fixedIdentityTime = time.Date(2026, 8, 1, 13, 0, 0, 0, time.UTC)

func TestEnsureCreatesPersistsAndSigns(t *testing.T) {
	local := openIdentityStore(t)
	secrets := platformfake.NewSecureStore()
	seed := bytes.Repeat([]byte{0x31}, ed25519.SeedSize)
	manager, err := NewManager(local, secrets, "identity/test", Options{Random: bytes.NewReader(seed), Clock: func() time.Time { return fixedIdentityTime }})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := manager.Ensure(context.Background())
	if err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	wantPublic := ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
	if !bytes.Equal(identity.PublicKey, wantPublic) || identity.PrivateKeyRef != "identity/test" {
		t.Fatalf("Ensure() returned wrong identity")
	}
	storedSeed, err := secrets.Get(context.Background(), "identity/test")
	if err != nil || !bytes.Equal(storedSeed, seed) || len(storedSeed) != ed25519.SeedSize {
		t.Fatal("SecureStore does not contain the seed")
	}
	clear(storedSeed)
	identity.PublicKey[0] ^= 0xff
	again, err := manager.Ensure(context.Background())
	if err != nil || !bytes.Equal(again.PublicKey, wantPublic) {
		t.Fatal("Ensure() was not idempotent")
	}
	message := []byte("synthetic enrollment challenge")
	signature, err := manager.Sign(context.Background(), message)
	if err != nil || !ed25519.Verify(wantPublic, message, signature) {
		t.Fatal("Sign() returned an invalid signature")
	}
	bound, err := manager.Bind(context.Background(), "owner", "node")
	if err != nil || bound.OwnerID != "owner" || bound.NodeID != "node" {
		t.Fatalf("Bind() = %#v, %v", bound, err)
	}
	if _, err := manager.Bind(context.Background(), "other-owner", "other-node"); !errors.Is(err, ErrMismatch) {
		t.Fatalf("rebind error = %v", err)
	}
}

func TestEnsureAdoptsOrphanSeed(t *testing.T) {
	local := openIdentityStore(t)
	secrets := platformfake.NewSecureStore()
	seed := bytes.Repeat([]byte{0x42}, ed25519.SeedSize)
	if err := secrets.Put(context.Background(), "identity/orphan", seed); err != nil {
		t.Fatal(err)
	}
	manager, _ := NewManager(local, secrets, "identity/orphan", Options{Random: bytes.NewReader(bytes.Repeat([]byte{0x99}, ed25519.SeedSize))})
	got, err := manager.Ensure(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
	if !bytes.Equal(got.PublicKey, want) {
		t.Fatal("orphan seed was replaced instead of adopted")
	}
}

func TestConcurrentEnsureCreatesOneIdentity(t *testing.T) {
	local := openIdentityStore(t)
	secrets := platformfake.NewSecureStore()
	random := &countingReader{data: bytes.Repeat([]byte{0x55}, ed25519.SeedSize)}
	manager, _ := NewManager(local, secrets, "identity/concurrent", Options{Random: random})
	var group sync.WaitGroup
	publicKeys := make(chan []byte, 16)
	for range 16 {
		group.Add(1)
		go func() {
			defer group.Done()
			identity, err := manager.Ensure(context.Background())
			if err != nil {
				t.Errorf("Ensure() error = %v", err)
				return
			}
			publicKeys <- identity.PublicKey
		}()
	}
	group.Wait()
	close(publicKeys)
	var first []byte
	for public := range publicKeys {
		if first == nil {
			first = public
		} else if !bytes.Equal(first, public) {
			t.Fatal("concurrent Ensure produced multiple identities")
		}
	}
	if random.BytesRead() != ed25519.SeedSize {
		t.Fatalf("random bytes read = %d, want %d", random.BytesRead(), ed25519.SeedSize)
	}
}

func TestIdentityRejectsMissingCorruptAndMismatchedSecrets(t *testing.T) {
	local := openIdentityStore(t)
	secrets := platformfake.NewSecureStore()
	manager, _ := NewManager(local, secrets, "identity/failure-canary", Options{Random: bytes.NewReader(bytes.Repeat([]byte{0x66}, ed25519.SeedSize))})
	if _, err := manager.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := secrets.Delete(context.Background(), "identity/failure-canary"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Load(context.Background()); !errors.Is(err, ErrMissing) || strings.Contains(err.Error(), "canary") {
		t.Fatalf("missing secret error = %v", err)
	}
	if err := secrets.Put(context.Background(), "identity/failure-canary", []byte("short")); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Load(context.Background()); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid secret error = %v", err)
	}
	if err := secrets.Put(context.Background(), "identity/failure-canary", bytes.Repeat([]byte{0x77}, ed25519.SeedSize)); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Load(context.Background()); !errors.Is(err, ErrMismatch) {
		t.Fatalf("mismatched secret error = %v", err)
	}
	other, _ := NewManager(local, secrets, "identity/other", Options{})
	if _, err := other.Load(context.Background()); !errors.Is(err, ErrMismatch) {
		t.Fatalf("SecretRef mismatch error = %v", err)
	}
}

func TestIdentityUnavailableAndBounds(t *testing.T) {
	local := openIdentityStore(t)
	manager, err := NewManager(local, unavailableSecureStore{}, "identity/test", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Ensure(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Ensure() error = %v", err)
	}
	if _, err := manager.Sign(context.Background(), make([]byte, MaxSigningMessageBytes+1)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("oversize Sign() error = %v", err)
	}
	if _, err := NewManager(local, unavailableSecureStore{}, platform.SecretRef("bad\nref"), Options{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid ref error = %v", err)
	}
}

func TestIdentityHonorsCanceledContext(t *testing.T) {
	local := openIdentityStore(t)
	manager, _ := NewManager(local, platformfake.NewSecureStore(), "identity/canceled", Options{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := manager.Ensure(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Ensure() error = %v", err)
	}
	if _, err := manager.Sign(ctx, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("Sign() error = %v", err)
	}
}

type unavailableSecureStore struct{}

func (unavailableSecureStore) Available() bool { return false }
func (unavailableSecureStore) Put(context.Context, platform.SecretRef, []byte) error {
	return platform.ErrUnavailable
}
func (unavailableSecureStore) Get(context.Context, platform.SecretRef) ([]byte, error) {
	return nil, platform.ErrUnavailable
}
func (unavailableSecureStore) Delete(context.Context, platform.SecretRef) error {
	return platform.ErrUnavailable
}

type countingReader struct {
	mu   sync.Mutex
	data []byte
	read int
}

func (r *countingReader) Read(target []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.read >= len(r.data) {
		return 0, io.EOF
	}
	count := copy(target, r.data[r.read:])
	r.read += count
	return count, nil
}

func (r *countingReader) BytesRead() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.read
}
