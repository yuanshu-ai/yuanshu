package v1

import (
	"context"
	"crypto/ed25519"
	"errors"
	"sync"
	"time"
)

var (
	ErrTrustKeyNotFound = errors.New("control trust key not found")
	ErrReplayDetected   = errors.New("control replay detected")
)

type KeyRef struct {
	OwnerID  string
	NodeID   string
	ClientID string
	KeyID    string
}

type TrustStatus string

const (
	TrustStatusActive  TrustStatus = "active"
	TrustStatusRevoked TrustStatus = "revoked"
)

type TrustedKey struct {
	PublicKey []byte
	Status    TrustStatus
}

type TrustStore interface {
	LookupControlKey(ctx context.Context, ref KeyRef) (TrustedKey, error)
}

type ReplayRecord struct {
	OwnerID       string
	NodeID        string
	MessageID     string
	ClientID      string
	KeyID         string
	Nonce         string
	Sequence      int64
	NonceRetainTo time.Time
}

type ReplayStore interface {
	CheckAndRecord(ctx context.Context, record ReplayRecord) error
}

// MemoryTrustStore is a concurrency-safe reference store for tests and
// ephemeral deployments. Persistent secure storage belongs to a later task.
type MemoryTrustStore struct {
	mu   sync.RWMutex
	keys map[KeyRef]TrustedKey
}

func NewMemoryTrustStore() *MemoryTrustStore {
	return &MemoryTrustStore{keys: make(map[KeyRef]TrustedKey)}
}

func (s *MemoryTrustStore) Set(ref KeyRef, key TrustedKey) error {
	if !validKeyRef(ref) || len(key.PublicKey) != ed25519.PublicKeySize || (key.Status != TrustStatusActive && key.Status != TrustStatusRevoked) {
		return errors.New("invalid control trust key")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keys[ref] = cloneTrustedKey(key)
	return nil
}

func (s *MemoryTrustStore) Revoke(ref KeyRef) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key, ok := s.keys[ref]
	if !ok {
		return ErrTrustKeyNotFound
	}
	key.Status = TrustStatusRevoked
	s.keys[ref] = key
	return nil
}

func (s *MemoryTrustStore) LookupControlKey(ctx context.Context, ref KeyRef) (TrustedKey, error) {
	if ctx == nil || ctx.Err() != nil {
		return TrustedKey{}, errors.New("trust lookup canceled")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	key, ok := s.keys[ref]
	if !ok {
		return TrustedKey{}, ErrTrustKeyNotFound
	}
	return cloneTrustedKey(key), nil
}

func cloneTrustedKey(key TrustedKey) TrustedKey {
	clone := key
	clone.PublicKey = append([]byte(nil), key.PublicKey...)
	return clone
}

func validKeyRef(ref KeyRef) bool {
	return ref.OwnerID != "" && ref.NodeID != "" && ref.ClientID != "" && ref.KeyID != ""
}

type replayMessageKey struct {
	ownerID   string
	nodeID    string
	messageID string
}

type replayNonceKey struct {
	ownerID  string
	nodeID   string
	clientID string
	keyID    string
	nonce    string
}

type replaySequenceKey struct {
	ownerID  string
	nodeID   string
	clientID string
	keyID    string
}

// MemoryReplayStore atomically checks all replay dimensions under one lock.
// It intentionally retains accepted identifiers for its full lifetime.
type MemoryReplayStore struct {
	mu        sync.Mutex
	messages  map[replayMessageKey]struct{}
	nonces    map[replayNonceKey]time.Time
	sequences map[replaySequenceKey]int64
}

func NewMemoryReplayStore() *MemoryReplayStore {
	return &MemoryReplayStore{
		messages:  make(map[replayMessageKey]struct{}),
		nonces:    make(map[replayNonceKey]time.Time),
		sequences: make(map[replaySequenceKey]int64),
	}
}

func (s *MemoryReplayStore) CheckAndRecord(ctx context.Context, record ReplayRecord) error {
	if ctx == nil || ctx.Err() != nil {
		return errors.New("replay check canceled")
	}
	if record.OwnerID == "" || record.NodeID == "" || record.MessageID == "" || record.ClientID == "" || record.KeyID == "" || record.Nonce == "" || record.Sequence < 0 || record.NonceRetainTo.IsZero() {
		return errors.New("invalid replay record")
	}

	messageKey := replayMessageKey{record.OwnerID, record.NodeID, record.MessageID}
	nonceKey := replayNonceKey{record.OwnerID, record.NodeID, record.ClientID, record.KeyID, record.Nonce}
	sequenceKey := replaySequenceKey{record.OwnerID, record.NodeID, record.ClientID, record.KeyID}

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.messages[messageKey]; exists {
		return ErrReplayDetected
	}
	if _, exists := s.nonces[nonceKey]; exists {
		return ErrReplayDetected
	}
	if highest, exists := s.sequences[sequenceKey]; exists && record.Sequence <= highest {
		return ErrReplayDetected
	}

	s.messages[messageKey] = struct{}{}
	s.nonces[nonceKey] = record.NonceRetainTo
	s.sequences[sequenceKey] = record.Sequence
	return nil
}
