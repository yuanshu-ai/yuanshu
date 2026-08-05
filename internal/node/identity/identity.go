// Package identity manages the long-lived Ed25519 identity of a Yuanshu Node.
package identity

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/yuanshu-ai/yuanshu/internal/node/store"
)

const (
	MaxSigningMessageBytes = 64 << 10
	FileKeyReference       = "file:identity.key"
)

var (
	ErrUnavailable = errors.New("node identity file storage is unavailable")
	ErrMissing     = errors.New("node identity key is missing")
	ErrInvalid     = errors.New("node identity is invalid")
	ErrMismatch    = errors.New("node identity metadata does not match its key")
)

type KeyStore interface {
	Available() bool
	Put(context.Context, []byte) error
	Get(context.Context) ([]byte, error)
	Delete(context.Context) error
}

type Options struct {
	Random io.Reader
	Clock  func() time.Time
}

type Identity struct {
	PublicKey     []byte
	PrivateKeyRef string
	OwnerID       string
	NodeID        string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

type Manager struct {
	store   *store.Store
	keys    KeyStore
	random  io.Reader
	clock   func() time.Time
	mu      sync.RWMutex
	private ed25519.PrivateKey
}

func NewManager(local *store.Store, keys KeyStore, options Options) (*Manager, error) {
	if local == nil || keys == nil {
		return nil, ErrInvalid
	}
	random := options.Random
	if random == nil {
		random = rand.Reader
	}
	clock := options.Clock
	if clock == nil {
		clock = time.Now
	}
	return &Manager{store: local, keys: keys, random: random, clock: clock}, nil
}

func (m *Manager) Ensure(ctx context.Context) (Identity, error) {
	if err := requireContext(ctx); err != nil {
		return Identity{}, err
	}
	if !m.keys.Available() {
		return Identity{}, ErrUnavailable
	}
	record, _, err := m.store.LoadOrCreateIdentity(ctx, func(ctx context.Context) (store.IdentityRecord, func(), error) {
		seed, err := m.keys.Get(ctx)
		created := false
		if errors.Is(err, ErrMissing) {
			seed = make([]byte, ed25519.SeedSize)
			if _, err := io.ReadFull(m.random, seed); err != nil {
				clear(seed)
				return store.IdentityRecord{}, nil, errors.New("node identity generation failed")
			}
			if err := m.keys.Put(ctx, seed); err != nil {
				clear(seed)
				return store.IdentityRecord{}, nil, classifyKeyError(err)
			}
			created = true
		} else if err != nil {
			return store.IdentityRecord{}, nil, classifyKeyError(err)
		}
		if len(seed) != ed25519.SeedSize {
			clear(seed)
			return store.IdentityRecord{}, nil, ErrInvalid
		}
		private := ed25519.NewKeyFromSeed(seed)
		public := append([]byte(nil), private.Public().(ed25519.PublicKey)...)
		clear(private)
		clear(seed)
		now := m.clock().UTC()
		var rollback func()
		if created {
			rollback = func() { _ = m.keys.Delete(context.Background()) }
		}
		return store.IdentityRecord{
			Algorithm: "ed25519", PublicKey: public, PrivateKeyRef: FileKeyReference, CreatedAt: now, UpdatedAt: now,
		}, rollback, nil
	})
	if err != nil {
		return Identity{}, sanitizeStoreError(err)
	}
	return m.verify(ctx, record)
}

func (m *Manager) Load(ctx context.Context) (Identity, error) {
	if err := requireContext(ctx); err != nil {
		return Identity{}, err
	}
	record, err := m.store.Identity(ctx)
	if errors.Is(err, store.ErrNotFound) {
		return Identity{}, ErrMissing
	}
	if err != nil {
		return Identity{}, sanitizeStoreError(err)
	}
	return m.verify(ctx, record)
}

func (m *Manager) Bind(ctx context.Context, ownerID, nodeID string) (Identity, error) {
	if err := requireContext(ctx); err != nil {
		return Identity{}, err
	}
	if err := m.store.BindIdentity(ctx, ownerID, nodeID); err != nil {
		return Identity{}, sanitizeStoreError(err)
	}
	return m.Load(ctx)
}

func (m *Manager) Sign(ctx context.Context, message []byte) ([]byte, error) {
	if err := requireContext(ctx); err != nil {
		return nil, err
	}
	if len(message) > MaxSigningMessageBytes {
		return nil, ErrInvalid
	}
	record, err := m.store.Identity(ctx)
	if errors.Is(err, store.ErrNotFound) {
		return nil, ErrMissing
	}
	if err != nil {
		return nil, sanitizeStoreError(err)
	}
	private, err := m.privateKey(ctx, record)
	if err != nil {
		return nil, err
	}
	signature := ed25519.Sign(private, append([]byte(nil), message...))
	clear(private)
	return signature, nil
}

// Close releases the in-memory private key. The backing file is intentionally
// retained so a normal Node restart preserves its device identity.
func (m *Manager) Close() {
	m.mu.Lock()
	clear(m.private)
	m.private = nil
	m.mu.Unlock()
}

func (m *Manager) verify(ctx context.Context, record store.IdentityRecord) (Identity, error) {
	private, err := m.privateKey(ctx, record)
	if err != nil {
		return Identity{}, err
	}
	public := private.Public().(ed25519.PublicKey)
	matches := bytes.Equal(public, record.PublicKey)
	clear(private)
	if !matches {
		return Identity{}, ErrMismatch
	}
	return Identity{
		PublicKey: append([]byte(nil), record.PublicKey...), PrivateKeyRef: record.PrivateKeyRef,
		OwnerID: record.OwnerID, NodeID: record.NodeID, CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt,
	}, nil
}

func (m *Manager) privateKey(ctx context.Context, record store.IdentityRecord) (ed25519.PrivateKey, error) {
	if record.PrivateKeyRef != FileKeyReference {
		return nil, ErrMismatch
	}
	m.mu.RLock()
	if len(m.private) == ed25519.PrivateKeySize {
		if !bytes.Equal(m.private.Public().(ed25519.PublicKey), record.PublicKey) {
			m.mu.RUnlock()
			return nil, ErrMismatch
		}
		private := append(ed25519.PrivateKey(nil), m.private...)
		m.mu.RUnlock()
		return private, nil
	}
	m.mu.RUnlock()
	if err := requireContext(ctx); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.private) == ed25519.PrivateKeySize {
		if !bytes.Equal(m.private.Public().(ed25519.PublicKey), record.PublicKey) {
			return nil, ErrMismatch
		}
		return append(ed25519.PrivateKey(nil), m.private...), nil
	}
	if !m.keys.Available() {
		return nil, ErrUnavailable
	}
	seed, err := m.keys.Get(ctx)
	if err != nil {
		return nil, classifyKeyError(err)
	}
	if len(seed) != ed25519.SeedSize {
		clear(seed)
		return nil, ErrInvalid
	}
	private := ed25519.NewKeyFromSeed(seed)
	clear(seed)
	if !bytes.Equal(private.Public().(ed25519.PublicKey), record.PublicKey) {
		clear(private)
		return nil, ErrMismatch
	}
	m.private = private
	return append(ed25519.PrivateKey(nil), private...), nil
}

func classifyKeyError(err error) error {
	switch {
	case errors.Is(err, context.Canceled):
		return context.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		return context.DeadlineExceeded
	case errors.Is(err, ErrUnavailable):
		return ErrUnavailable
	case errors.Is(err, ErrMissing):
		return ErrMissing
	case errors.Is(err, ErrInvalid):
		return ErrInvalid
	default:
		return ErrUnavailable
	}
}

func sanitizeStoreError(err error) error {
	switch {
	case errors.Is(err, context.Canceled):
		return context.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		return context.DeadlineExceeded
	case errors.Is(err, store.ErrNotFound):
		return ErrMissing
	case errors.Is(err, store.ErrConflict):
		return ErrMismatch
	case errors.Is(err, store.ErrInvalid):
		return ErrInvalid
	default:
		return errors.New("node identity storage failed")
	}
}

func requireContext(ctx context.Context) error {
	if ctx == nil {
		return context.Canceled
	}
	return ctx.Err()
}
