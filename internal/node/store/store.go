package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
)

const CurrentSchemaVersion = 8

type Inspection struct {
	SchemaVersion int
	QuickCheck    string
}

// Inspect opens an existing Node database read-only and never creates or migrates it.
func Inspect(ctx context.Context, path string) (Inspection, error) {
	if ctx == nil || ctx.Err() != nil {
		if ctx == nil {
			return Inspection{}, context.Canceled
		}
		return Inspection{}, ctx.Err()
	}
	if !filepath.IsAbs(path) {
		return Inspection{}, ErrInvalid
	}
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Inspection{}, ErrNotFound
		}
		return Inspection{}, internal("inspection")
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return Inspection{}, ErrInvalid
	}
	uri := (&url.URL{Scheme: "file", Opaque: filepath.ToSlash(path), RawQuery: "mode=ro"}).String()
	db, err := sql.Open("sqlite3", uri)
	if err != nil {
		return Inspection{}, ErrCorrupt
	}
	defer db.Close()
	var result Inspection
	if err := db.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&result.QuickCheck); err != nil || result.QuickCheck != "ok" {
		return Inspection{}, ErrCorrupt
	}
	if err := db.QueryRowContext(ctx, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&result.SchemaVersion); err != nil {
		return Inspection{}, ErrCorrupt
	}
	if result.SchemaVersion < 1 || result.SchemaVersion > CurrentSchemaVersion {
		return Inspection{}, ErrCorrupt
	}
	return result, nil
}

type Options struct {
	Clock func() time.Time
}

type Store struct {
	db    *sql.DB
	clock func() time.Time
	mu    sync.RWMutex
	dead  bool
}

func Open(ctx context.Context, path string, options Options) (*Store, error) {
	if ctx == nil {
		return nil, context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if path == "" || !filepath.IsAbs(path) {
		return nil, ErrInvalid
	}
	parent := filepath.Dir(path)
	parentInfo, err := os.Stat(parent)
	if err != nil || !parentInfo.IsDir() {
		return nil, errors.New("node store parent directory is unavailable")
	}
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return nil, ErrInvalid
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("node store path is unavailable")
	}

	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, errors.New("node store file is unavailable")
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return nil, errors.New("node store permissions could not be applied")
	}
	if err := file.Close(); err != nil {
		return nil, errors.New("node store file could not be prepared")
	}

	db, err := sql.Open("sqlite3", sqliteDSN(path))
	if err != nil {
		return nil, ErrCorrupt
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	store := &Store{db: db, clock: options.Clock}
	if store.clock == nil {
		store.clock = time.Now
	}
	if err := store.initialize(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func sqliteDSN(path string) string {
	return filepath.ToSlash(path)
}

func (s *Store) initialize(ctx context.Context) error {
	if err := s.db.PingContext(ctx); err != nil {
		return ErrCorrupt
	}
	pragmas := []string{
		"PRAGMA foreign_keys = ON",
		"PRAGMA synchronous = FULL",
		"PRAGMA busy_timeout = 5000",
	}
	for _, statement := range pragmas {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return ErrCorrupt
		}
	}
	var journalMode string
	if err := s.db.QueryRowContext(ctx, "PRAGMA journal_mode = WAL").Scan(&journalMode); err != nil || journalMode != "wal" {
		return errors.New("node store WAL mode is unavailable")
	}
	if err := runMigrations(ctx, s.db, s.clock().UTC()); err != nil {
		return err
	}
	var check string
	if err := s.db.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&check); err != nil || check != "ok" {
		return ErrCorrupt
	}
	return nil
}

func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	if s.dead {
		s.mu.Unlock()
		return nil
	}
	s.dead = true
	s.mu.Unlock()
	if err := s.db.Close(); err != nil {
		return errors.New("node store close failed")
	}
	return nil
}

func (s *Store) database() (*sql.DB, error) {
	if s == nil {
		return nil, ErrClosed
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.dead {
		return nil, ErrClosed
	}
	return s.db, nil
}

func timestamp(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}

func internal(operation string) error {
	return fmt.Errorf("node store %s failed", operation)
}

func requireContext(ctx context.Context) error {
	if ctx == nil {
		return context.Canceled
	}
	return ctx.Err()
}
