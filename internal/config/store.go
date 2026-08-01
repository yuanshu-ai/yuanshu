package config

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/pelletier/go-toml/v2"
)

type LoadResult struct {
	Config              Config
	RecoveredFromBackup bool
	Migrated            bool
}

type FileStore struct {
	path        string
	backupPath  string
	lock        *sync.Mutex
	writeAtomic func(context.Context, string, []byte) error
}

var fileLocks sync.Map

func NewFileStore(path string) (*FileStore, error) {
	if path == "" {
		return nil, configError("path", ErrInvalid)
	}
	cleaned, err := filepath.Abs(path)
	if err != nil {
		return nil, configError("path", ErrInvalid)
	}
	cleaned = filepath.Clean(cleaned)
	lockValue, _ := fileLocks.LoadOrStore(cleaned, &sync.Mutex{})
	return &FileStore{
		path:        cleaned,
		backupPath:  cleaned + ".bak",
		lock:        lockValue.(*sync.Mutex),
		writeAtomic: atomicWriteFile,
	}, nil
}

func (s *FileStore) Load(ctx context.Context) (LoadResult, error) {
	if err := contextError(ctx); err != nil {
		return LoadResult{}, err
	}
	s.lock.Lock()
	defer s.lock.Unlock()

	primary, err := loadFile(ctx, s.path)
	if err == nil {
		return LoadResult{Config: primary.config, Migrated: primary.migrated}, nil
	}
	if !recoverableLoadError(err) {
		return LoadResult{}, err
	}
	backup, backupErr := loadFile(ctx, s.backupPath)
	if backupErr == nil {
		return LoadResult{
			Config:              backup.config,
			RecoveredFromBackup: true,
			Migrated:            backup.migrated,
		}, nil
	}
	if errors.Is(backupErr, ErrNotFound) {
		return LoadResult{}, err
	}
	return LoadResult{}, backupErr
}

func (s *FileStore) Save(ctx context.Context, value Config) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if err := Validate(value); err != nil {
		return err
	}
	encoded, err := toml.Marshal(value)
	if err != nil || len(encoded) > MaxFileBytes {
		if len(encoded) > MaxFileBytes {
			return configError("encoding", ErrTooLarge)
		}
		return configError("encoding", ErrInvalid)
	}

	s.lock.Lock()
	defer s.lock.Unlock()
	if err := validateParentDirectory(s.path); err != nil {
		return err
	}

	current, loadErr := loadFile(ctx, s.path)
	switch {
	case loadErr == nil:
		if err := s.safeWrite(ctx, s.backupPath, current.raw); err != nil {
			return err
		}
	case errors.Is(loadErr, ErrNotFound), errors.Is(loadErr, ErrInvalid), errors.Is(loadErr, ErrTooLarge):
		// First save or a replacement of a corrupt primary preserves any
		// existing known-good backup.
	default:
		return loadErr
	}
	if err := contextError(ctx); err != nil {
		return err
	}
	return s.safeWrite(ctx, s.path, encoded)
}

func (s *FileStore) safeWrite(ctx context.Context, path string, value []byte) error {
	if err := s.writeAtomic(ctx, path, value); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		return configError("save", ErrIO)
	}
	return nil
}

type loadedFile struct {
	config   Config
	raw      []byte
	migrated bool
}

func loadFile(ctx context.Context, path string) (loadedFile, error) {
	if err := contextError(ctx); err != nil {
		return loadedFile{}, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return loadedFile{}, configError("load", ErrNotFound)
		}
		return loadedFile{}, configError("load", ErrIO)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return loadedFile{}, configError("load", ErrUnsafeFile)
	}
	if info.Size() > MaxFileBytes {
		return loadedFile{}, configError("load", ErrTooLarge)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return loadedFile{}, configError("load", ErrIO)
	}
	if len(raw) > MaxFileBytes {
		return loadedFile{}, configError("load", ErrTooLarge)
	}
	value, migrated, err := decode(raw)
	if err != nil {
		return loadedFile{}, err
	}
	return loadedFile{config: value, raw: append([]byte(nil), raw...), migrated: migrated}, nil
}

type versionHeader struct {
	ConfigVersion *int `toml:"config_version"`
}

type versionStep func([]byte) ([]byte, error)

var versionSteps = map[int]versionStep{
	CurrentVersion: func(raw []byte) ([]byte, error) {
		return append([]byte(nil), raw...), nil
	},
}

func decode(raw []byte) (Config, bool, error) {
	var header versionHeader
	if err := toml.Unmarshal(raw, &header); err != nil || header.ConfigVersion == nil || *header.ConfigVersion < 1 {
		return Config{}, false, configError("decoding", ErrInvalid)
	}
	if *header.ConfigVersion > CurrentVersion {
		return Config{}, false, configError("version", ErrUnsupportedVersion)
	}
	step, ok := versionSteps[*header.ConfigVersion]
	if !ok {
		return Config{}, false, configError("version", ErrUnsupportedVersion)
	}
	normalized, err := step(raw)
	if err != nil {
		return Config{}, false, configError("migration", ErrInvalid)
	}
	var document map[string]any
	if err := toml.Unmarshal(normalized, &document); err != nil {
		return Config{}, false, configError("decoding", ErrInvalid)
	}
	if err := validateSchema(document); err != nil {
		return Config{}, false, err
	}
	var value Config
	decoder := toml.NewDecoder(bytes.NewReader(normalized)).DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return Config{}, false, configError("decoding", ErrInvalid)
	}
	if err := Validate(value); err != nil {
		return Config{}, false, err
	}
	return value, *header.ConfigVersion != CurrentVersion, nil
}

func validateParentDirectory(path string) error {
	parent := filepath.Dir(path)
	info, err := os.Stat(parent)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return configError("path", ErrNotFound)
		}
		return configError("path", ErrIO)
	}
	if !info.IsDir() {
		return configError("path", ErrUnsafeFile)
	}
	return nil
}

func recoverableLoadError(err error) bool {
	return errors.Is(err, ErrNotFound) || errors.Is(err, ErrInvalid) || errors.Is(err, ErrTooLarge)
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return context.Canceled
	}
	return ctx.Err()
}

func atomicWriteFile(ctx context.Context, path string, value []byte) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	parent := filepath.Dir(path)
	temporary, err := os.CreateTemp(parent, ".yuanshu-config-*")
	if err != nil {
		return err
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
		return err
	}
	if _, err := io.Copy(temporary, bytes.NewReader(value)); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := contextError(ctx); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	committed = true
	return syncDirectory(parent)
}
