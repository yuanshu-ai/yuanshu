//go:build linux

package platform

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

const linuxSecretMagic = "YSSEC1"

type linuxEncryptedStore struct {
	root string
	mu   sync.RWMutex
	key  []byte
}

func newLinuxEncryptedStore(root, keyFile string) (*linuxEncryptedStore, error) {
	info, err := os.Lstat(keyFile)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("platform master key is unavailable")
	}
	key, err := os.ReadFile(keyFile)
	if err != nil || len(key) != 32 {
		clear(key)
		return nil, errors.New("platform master key is invalid")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		clear(key)
		return nil, errors.New("platform secret storage is unavailable")
	}
	rootInfo, err := os.Lstat(root)
	if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 || os.Chmod(root, 0o700) != nil {
		clear(key)
		return nil, errors.New("platform secret storage is unavailable")
	}
	return &linuxEncryptedStore{root: root, key: key}, nil
}

func (*linuxEncryptedStore) Available() bool { return true }

func (s *linuxEncryptedStore) Put(ctx context.Context, ref SecretRef, secret []byte) error {
	if err := validateLinuxSecretInput(ctx, ref); err != nil || len(secret) == 0 {
		return ErrInvalidArgument
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.key) != 32 {
		return ErrUnavailable
	}
	block, err := aes.NewCipher(s.key)
	if err != nil {
		return ErrUnavailable
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return ErrUnavailable
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return ErrUnavailable
	}
	aad := append([]byte("yuanshu-secret-v1\x00"), []byte(ref)...)
	encoded := append([]byte(linuxSecretMagic), nonce...)
	encoded = aead.Seal(encoded, nonce, secret, aad)
	clear(aad)
	defer clear(encoded)
	return s.atomicWrite(ctx, s.path(ref), encoded)
}

func (s *linuxEncryptedStore) Get(ctx context.Context, ref SecretRef) ([]byte, error) {
	if err := validateLinuxSecretInput(ctx, ref); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.key) != 32 {
		return nil, ErrUnavailable
	}
	path := s.path(ref)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("platform secret is unavailable")
	}
	encoded, err := os.ReadFile(path)
	if err != nil || len(encoded) < len(linuxSecretMagic)+12+16 || string(encoded[:len(linuxSecretMagic)]) != linuxSecretMagic {
		clear(encoded)
		return nil, errors.New("platform secret is invalid")
	}
	defer clear(encoded)
	block, err := aes.NewCipher(s.key)
	if err != nil {
		return nil, ErrUnavailable
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, ErrUnavailable
	}
	offset := len(linuxSecretMagic)
	nonce := encoded[offset : offset+aead.NonceSize()]
	aad := append([]byte("yuanshu-secret-v1\x00"), []byte(ref)...)
	plain, err := aead.Open(nil, nonce, encoded[offset+aead.NonceSize():], aad)
	clear(aad)
	if err != nil {
		return nil, errors.New("platform secret is invalid")
	}
	return plain, nil
}

func (s *linuxEncryptedStore) Delete(ctx context.Context, ref SecretRef) error {
	if err := validateLinuxSecretInput(ctx, ref); err != nil {
		return err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	err := os.Remove(s.path(ref))
	if errors.Is(err, os.ErrNotExist) {
		return ErrNotFound
	}
	if err != nil {
		return errors.New("platform secret could not be deleted")
	}
	return syncDirectory(s.root)
}

func (s *linuxEncryptedStore) Close() error {
	s.mu.Lock()
	clear(s.key)
	s.key = nil
	s.mu.Unlock()
	return nil
}

func (s *linuxEncryptedStore) path(ref SecretRef) string {
	digest := sha256.Sum256([]byte(ref))
	return filepath.Join(s.root, strings.ToLower(fmtHex(digest[:]))+".secret")
}

func (s *linuxEncryptedStore) atomicWrite(ctx context.Context, target string, value []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(s.root, ".secret-*")
	if err != nil {
		return errors.New("platform secret could not be saved")
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	fail := func() error {
		_ = temporary.Close()
		return errors.New("platform secret could not be saved")
	}
	if temporary.Chmod(0o600) != nil {
		return fail()
	}
	if _, err := temporary.Write(value); err != nil {
		return fail()
	}
	if err := ctx.Err(); err != nil {
		_ = temporary.Close()
		return err
	}
	if temporary.Sync() != nil || temporary.Close() != nil || os.Rename(temporaryPath, target) != nil {
		return errors.New("platform secret could not be saved")
	}
	return syncDirectory(s.root)
}

func validateLinuxSecretInput(ctx context.Context, ref SecretRef) error {
	if ctx == nil {
		return context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	value := string(ref)
	if value == "" || len(value) > 256 || !utf8.ValidString(value) || strings.TrimSpace(value) != value || strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return ErrInvalidArgument
	}
	return nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return errors.New("platform storage sync failed")
	}
	defer directory.Close()
	if directory.Sync() != nil {
		return errors.New("platform storage sync failed")
	}
	return nil
}

func fmtHex(value []byte) string {
	const alphabet = "0123456789abcdef"
	encoded := make([]byte, len(value)*2)
	for index, item := range value {
		encoded[index*2], encoded[index*2+1] = alphabet[item>>4], alphabet[item&15]
	}
	return string(encoded)
}
