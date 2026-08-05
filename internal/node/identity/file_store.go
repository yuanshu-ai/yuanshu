package identity

import (
	"context"
	"crypto/ed25519"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// FileKeyStore keeps the Node device seed in the private Node data directory.
// The path is deliberately not configurable by an untrusted caller: Node
// construction supplies the fixed identity.key basename.
type FileKeyStore struct {
	path string
	mu   *sync.Mutex
}

var fileKeyLocks sync.Map

func NewFileKeyStore(path string) (*FileKeyStore, error) {
	if path == "" || !filepath.IsAbs(path) || filepath.Base(path) != "identity.key" {
		return nil, ErrInvalid
	}
	cleaned := filepath.Clean(path)
	lock, _ := fileKeyLocks.LoadOrStore(cleaned, &sync.Mutex{})
	return &FileKeyStore{path: cleaned, mu: lock.(*sync.Mutex)}, nil
}

func (*FileKeyStore) Available() bool { return true }

func (s *FileKeyStore) Get(ctx context.Context) ([]byte, error) {
	if err := requireContext(ctx); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := checkPrivateDirectory(filepath.Dir(s.path), true); err != nil {
		return nil, err
	}
	info, err := os.Lstat(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrMissing
	}
	if err != nil {
		return nil, ErrUnavailable
	}
	if err := checkPrivateKeyFile(info); err != nil {
		return nil, err
	}
	seed, err := os.ReadFile(s.path)
	if err != nil {
		return nil, ErrUnavailable
	}
	if len(seed) != ed25519.SeedSize {
		clear(seed)
		return nil, ErrInvalid
	}
	return seed, nil
}

func (s *FileKeyStore) Put(ctx context.Context, seed []byte) error {
	if err := requireContext(ctx); err != nil {
		return err
	}
	if len(seed) != ed25519.SeedSize {
		return ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	parent := filepath.Dir(s.path)
	if err := checkPrivateDirectory(parent, true); err != nil {
		return err
	}
	if info, err := os.Lstat(s.path); err == nil {
		if checkErr := checkPrivateKeyFile(info); checkErr != nil {
			return checkErr
		}
		return ErrInvalid
	} else if !errors.Is(err, os.ErrNotExist) {
		return ErrUnavailable
	}
	temporary, err := os.CreateTemp(parent, ".identity.key-*")
	if err != nil {
		return ErrUnavailable
	}
	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		_ = temporary.Close()
		if !committed {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return ErrUnavailable
	}
	if _, err := temporary.Write(seed); err != nil {
		return ErrUnavailable
	}
	if err := temporary.Sync(); err != nil {
		return ErrUnavailable
	}
	if err := temporary.Close(); err != nil {
		return ErrUnavailable
	}
	if err := replaceIdentityFile(temporaryPath, s.path); err != nil {
		return ErrUnavailable
	}
	committed = true
	_ = syncPrivateDirectory(parent)
	return nil
}

func (s *FileKeyStore) Delete(ctx context.Context) error {
	if err := requireContext(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	info, err := os.Lstat(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return ErrMissing
	}
	if err != nil {
		return ErrUnavailable
	}
	if err := checkPrivateKeyFile(info); err != nil {
		return err
	}
	if err := os.Remove(s.path); err != nil {
		return ErrUnavailable
	}
	_ = syncPrivateDirectory(filepath.Dir(s.path))
	return nil
}

func checkPrivateDirectory(path string, create bool) error {
	if create {
		if err := os.MkdirAll(path, 0o700); err != nil {
			return ErrUnavailable
		}
	}
	if err := checkDirectoryComponents(path); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ErrMissing
		}
		return ErrUnavailable
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ErrInvalid
	}
	if err := infoPrivateDirectory(path, info); err != nil {
		return err
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return ErrUnavailable
	}
	return nil
}

func checkDirectoryComponents(path string) error {
	cleaned := filepath.Clean(path)
	volume := filepath.VolumeName(cleaned)
	remainder := strings.TrimPrefix(cleaned, volume)
	current := volume
	if strings.HasPrefix(remainder, string(filepath.Separator)) {
		current += string(filepath.Separator)
		remainder = strings.TrimPrefix(remainder, string(filepath.Separator))
	}
	for _, component := range strings.Split(remainder, string(filepath.Separator)) {
		if component == "" || component == "." {
			continue
		}
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			return ErrMissing
		}
		if err != nil {
			return ErrUnavailable
		}
		if info.Mode()&os.ModeSymlink != 0 {
			if !trustedDirectorySymlink(current, info) {
				return ErrInvalid
			}
			target, targetErr := os.Stat(current)
			if targetErr != nil || !target.IsDir() {
				return ErrInvalid
			}
			continue
		}
		if !info.IsDir() {
			return ErrInvalid
		}
	}
	return nil
}

func checkPrivateKeyFile(info os.FileInfo) error {
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return ErrInvalid
	}
	if info.Size() != ed25519.SeedSize || info.Mode().Perm()&0o077 != 0 {
		return ErrInvalid
	}
	if err := infoPrivateKey(info); err != nil {
		return err
	}
	return nil
}
