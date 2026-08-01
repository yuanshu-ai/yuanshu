package fake

import (
	"context"
	"sync"

	platformpkg "github.com/yuanshu-ai/yuanshu/internal/platform"
)

type SecureStore struct {
	failure injectedFailure
	mu      sync.RWMutex
	secrets map[platformpkg.SecretRef][]byte
}

var _ platformpkg.SecureStore = (*SecureStore)(nil)

func NewSecureStore() *SecureStore {
	return &SecureStore{secrets: make(map[platformpkg.SecretRef][]byte)}
}

func (*SecureStore) Available() bool { return true }

func (s *SecureStore) SetError(err error) { s.failure.set(err) }

func (s *SecureStore) Put(ctx context.Context, ref platformpkg.SecretRef, secret []byte) error {
	if err := s.failure.get(ctx); err != nil {
		return err
	}
	if ref == "" {
		return platformpkg.ErrInvalidArgument
	}
	copyOfSecret := append([]byte(nil), secret...)
	s.mu.Lock()
	s.secrets[ref] = copyOfSecret
	s.mu.Unlock()
	return nil
}

func (s *SecureStore) Get(ctx context.Context, ref platformpkg.SecretRef) ([]byte, error) {
	if err := s.failure.get(ctx); err != nil {
		return nil, err
	}
	if ref == "" {
		return nil, platformpkg.ErrInvalidArgument
	}
	s.mu.RLock()
	secret, ok := s.secrets[ref]
	copyOfSecret := append([]byte(nil), secret...)
	s.mu.RUnlock()
	if !ok {
		return nil, platformpkg.ErrNotFound
	}
	return copyOfSecret, nil
}

func (s *SecureStore) Delete(ctx context.Context, ref platformpkg.SecretRef) error {
	if err := s.failure.get(ctx); err != nil {
		return err
	}
	if ref == "" {
		return platformpkg.ErrInvalidArgument
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.secrets[ref]; !ok {
		return platformpkg.ErrNotFound
	}
	delete(s.secrets, ref)
	return nil
}
