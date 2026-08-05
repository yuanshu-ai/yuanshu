package identity

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"io"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/yuanshu-ai/yuanshu/internal/node/store"
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
	keys := &memoryKeyStore{}
	seed := bytes.Repeat([]byte{0x31}, ed25519.SeedSize)
	manager, err := NewManager(local, keys, Options{Random: bytes.NewReader(seed), Clock: func() time.Time { return fixedIdentityTime }})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	identity, err := manager.Ensure(context.Background())
	if err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	wantPublic := ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
	if !bytes.Equal(identity.PublicKey, wantPublic) || identity.PrivateKeyRef != FileKeyReference {
		t.Fatalf("Ensure() returned wrong identity")
	}
	storedSeed, err := keys.Get(context.Background())
	if err != nil || !bytes.Equal(storedSeed, seed) || len(storedSeed) != ed25519.SeedSize {
		t.Fatal("key store does not contain the seed")
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

func TestEnsureWithFileKeyStore(t *testing.T) {
	root := t.TempDir()
	local, err := store.Open(context.Background(), filepath.Join(root, "node.db"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close()
	keys, err := NewFileKeyStore(filepath.Join(root, "identity.key"))
	if err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(local, keys, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	if _, err := manager.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureAdoptsOrphanSeed(t *testing.T) {
	local := openIdentityStore(t)
	keys := &memoryKeyStore{}
	seed := bytes.Repeat([]byte{0x42}, ed25519.SeedSize)
	if err := keys.Put(context.Background(), seed); err != nil {
		t.Fatal(err)
	}
	manager, _ := NewManager(local, keys, Options{Random: bytes.NewReader(bytes.Repeat([]byte{0x99}, ed25519.SeedSize))})
	defer manager.Close()
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
	keys := &memoryKeyStore{}
	random := &countingReader{data: bytes.Repeat([]byte{0x55}, ed25519.SeedSize)}
	manager, _ := NewManager(local, keys, Options{Random: random})
	defer manager.Close()
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

func TestIdentityRejectsMissingCorruptAndMismatchedKeys(t *testing.T) {
	local := openIdentityStore(t)
	keys := &memoryKeyStore{}
	manager, _ := NewManager(local, keys, Options{Random: bytes.NewReader(bytes.Repeat([]byte{0x66}, ed25519.SeedSize))})
	defer manager.Close()
	if _, err := manager.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	manager.Close()
	if err := keys.Delete(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Load(context.Background()); !errors.Is(err, ErrMissing) {
		t.Fatalf("missing key error = %v", err)
	}
	if err := keys.Put(context.Background(), []byte("short")); err != nil {
		t.Fatal(err)
	}
	manager = mustNewManager(t, local, keys)
	if _, err := manager.Load(context.Background()); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid key error = %v", err)
	}
	if err := keys.Put(context.Background(), bytes.Repeat([]byte{0x77}, ed25519.SeedSize)); err != nil {
		t.Fatal(err)
	}
	manager = mustNewManager(t, local, keys)
	if _, err := manager.Load(context.Background()); !errors.Is(err, ErrMismatch) {
		t.Fatalf("mismatched key error = %v", err)
	}
}

func mustNewManager(t *testing.T, local *store.Store, keys KeyStore) *Manager {
	t.Helper()
	manager, err := NewManager(local, keys, Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)
	return manager
}

func TestSignDoesNotReadKeyFileForEverySignature(t *testing.T) {
	local := openIdentityStore(t)
	keys := &memoryKeyStore{}
	manager, _ := NewManager(local, keys, Options{Random: bytes.NewReader(bytes.Repeat([]byte{0x12}, ed25519.SeedSize))})
	defer manager.Close()
	if _, err := manager.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	gets := keys.GetCount()
	for range 5 {
		if _, err := manager.Sign(context.Background(), []byte("challenge")); err != nil {
			t.Fatal(err)
		}
	}
	if keys.GetCount() != gets {
		t.Fatalf("key reads after cache = %d, want %d", keys.GetCount(), gets)
	}
}

func TestIdentityUnavailableAndBounds(t *testing.T) {
	local := openIdentityStore(t)
	manager, err := NewManager(local, unavailableKeyStore{}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Ensure(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Ensure() error = %v", err)
	}
	if _, err := manager.Sign(context.Background(), make([]byte, MaxSigningMessageBytes+1)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("oversize Sign() error = %v", err)
	}
}

func TestIdentityHonorsCanceledContext(t *testing.T) {
	local := openIdentityStore(t)
	manager, _ := NewManager(local, &memoryKeyStore{}, Options{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := manager.Ensure(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Ensure() error = %v", err)
	}
	if _, err := manager.Sign(ctx, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("Sign() error = %v", err)
	}
}

type memoryKeyStore struct {
	mu        sync.Mutex
	seed      []byte
	available bool
	gets      int
}

func (s *memoryKeyStore) Available() bool { return true }

func (s *memoryKeyStore) Put(_ context.Context, seed []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seed = append([]byte(nil), seed...)
	return nil
}

func (s *memoryKeyStore) Get(_ context.Context) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gets++
	if len(s.seed) == 0 {
		return nil, ErrMissing
	}
	return append([]byte(nil), s.seed...), nil
}

func (s *memoryKeyStore) Delete(_ context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	clear(s.seed)
	s.seed = nil
	return nil
}

func (s *memoryKeyStore) GetCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gets
}

type unavailableKeyStore struct{}

func (unavailableKeyStore) Available() bool { return false }
func (unavailableKeyStore) Put(context.Context, []byte) error {
	return ErrUnavailable
}
func (unavailableKeyStore) Get(context.Context) ([]byte, error) { return nil, ErrUnavailable }
func (unavailableKeyStore) Delete(context.Context) error        { return ErrUnavailable }

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
