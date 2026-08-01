// Package identity manages the long-lived Ed25519 identity of a Yuanshu Node.
package identity

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/yuanshu-ai/yuanshu/internal/node/store"
	"github.com/yuanshu-ai/yuanshu/internal/platform"
)

const MaxSigningMessageBytes = 64 << 10

var (
	ErrUnavailable = errors.New("node identity secure storage is unavailable")
	ErrMissing     = errors.New("node identity secret is missing")
	ErrInvalid     = errors.New("node identity is invalid")
	ErrMismatch    = errors.New("node identity metadata does not match its secret")
)

type Options struct {
	Random io.Reader
	Clock  func() time.Time
}

type Identity struct {
	PublicKey     []byte
	PrivateKeyRef platform.SecretRef
	OwnerID       string
	NodeID        string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

type Manager struct {
	store   *store.Store
	secrets platform.SecureStore
	ref     platform.SecretRef
	random  io.Reader
	clock   func() time.Time
}

func NewManager(local *store.Store, secrets platform.SecureStore, ref platform.SecretRef, options Options) (*Manager, error) {
	if local == nil || secrets == nil || !validSecretRef(ref) {
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
	return &Manager{store: local, secrets: secrets, ref: ref, random: random, clock: clock}, nil
}

func (m *Manager) Ensure(ctx context.Context) (Identity, error) {
	if err := requireContext(ctx); err != nil {
		return Identity{}, err
	}
	if !m.secrets.Available() {
		return Identity{}, ErrUnavailable
	}
	record, _, err := m.store.LoadOrCreateIdentity(ctx, func(ctx context.Context) (store.IdentityRecord, func(), error) {
		seed, err := m.secrets.Get(ctx, m.ref)
		created := false
		if errors.Is(err, platform.ErrNotFound) {
			seed = make([]byte, ed25519.SeedSize)
			if _, err := io.ReadFull(m.random, seed); err != nil {
				clear(seed)
				return store.IdentityRecord{}, nil, errors.New("node identity generation failed")
			}
			if err := m.secrets.Put(ctx, m.ref, seed); err != nil {
				clear(seed)
				return store.IdentityRecord{}, nil, classifySecretError(err)
			}
			created = true
		} else if err != nil {
			return store.IdentityRecord{}, nil, classifySecretError(err)
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
			rollback = func() { _ = m.secrets.Delete(context.Background(), m.ref) }
		}
		return store.IdentityRecord{
			Algorithm: "ed25519", PublicKey: public, PrivateKeyRef: string(m.ref), CreatedAt: now, UpdatedAt: now,
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
	seed, err := m.loadSeed(ctx, record)
	if err != nil {
		return nil, err
	}
	private := ed25519.NewKeyFromSeed(seed)
	clear(seed)
	signature := ed25519.Sign(private, append([]byte(nil), message...))
	clear(private)
	return signature, nil
}

func (m *Manager) verify(ctx context.Context, record store.IdentityRecord) (Identity, error) {
	seed, err := m.loadSeed(ctx, record)
	if err != nil {
		return Identity{}, err
	}
	private := ed25519.NewKeyFromSeed(seed)
	clear(seed)
	public := private.Public().(ed25519.PublicKey)
	matches := bytes.Equal(public, record.PublicKey)
	clear(private)
	if !matches {
		return Identity{}, ErrMismatch
	}
	return Identity{
		PublicKey: append([]byte(nil), record.PublicKey...), PrivateKeyRef: platform.SecretRef(record.PrivateKeyRef),
		OwnerID: record.OwnerID, NodeID: record.NodeID, CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt,
	}, nil
}

func (m *Manager) loadSeed(ctx context.Context, record store.IdentityRecord) ([]byte, error) {
	if platform.SecretRef(record.PrivateKeyRef) != m.ref {
		return nil, ErrMismatch
	}
	if !m.secrets.Available() {
		return nil, ErrUnavailable
	}
	seed, err := m.secrets.Get(ctx, m.ref)
	if err != nil {
		return nil, classifySecretError(err)
	}
	if len(seed) != ed25519.SeedSize {
		clear(seed)
		return nil, ErrInvalid
	}
	return seed, nil
}

func classifySecretError(err error) error {
	switch {
	case errors.Is(err, context.Canceled):
		return context.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		return context.DeadlineExceeded
	case errors.Is(err, platform.ErrUnavailable):
		return ErrUnavailable
	case errors.Is(err, platform.ErrNotFound):
		return ErrMissing
	default:
		return errors.New("node identity secure storage failed")
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

func validSecretRef(ref platform.SecretRef) bool {
	text := string(ref)
	return text != "" && len(text) <= 512 && strings.IndexFunc(text, func(r rune) bool { return r < 0x20 || r == 0x7f }) < 0
}
