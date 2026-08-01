package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type IdentityRecord struct {
	Algorithm     string
	PublicKey     []byte
	PrivateKeyRef string
	OwnerID       string
	NodeID        string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

type IdentityFactory func(context.Context) (record IdentityRecord, rollback func(), err error)

func (s *Store) Identity(ctx context.Context) (IdentityRecord, error) {
	if err := requireContext(ctx); err != nil {
		return IdentityRecord{}, err
	}
	db, err := s.database()
	if err != nil {
		return IdentityRecord{}, err
	}
	record, err := scanIdentity(db.QueryRowContext(ctx, `SELECT algorithm, public_key, private_key_ref,
		COALESCE(owner_id, ''), COALESCE(node_id, ''), created_at, updated_at FROM identity WHERE singleton = 1`))
	if errors.Is(err, sql.ErrNoRows) {
		return IdentityRecord{}, ErrNotFound
	}
	if err != nil {
		return IdentityRecord{}, internal("identity read")
	}
	return record, nil
}

func (s *Store) LoadOrCreateIdentity(ctx context.Context, factory IdentityFactory) (IdentityRecord, bool, error) {
	if err := requireContext(ctx); err != nil {
		return IdentityRecord{}, false, err
	}
	if factory == nil {
		return IdentityRecord{}, false, ErrInvalid
	}
	db, err := s.database()
	if err != nil {
		return IdentityRecord{}, false, err
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return IdentityRecord{}, false, internal("identity initialization")
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return IdentityRecord{}, false, internal("identity initialization")
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	existing, err := scanIdentity(conn.QueryRowContext(ctx, `SELECT algorithm, public_key, private_key_ref,
		COALESCE(owner_id, ''), COALESCE(node_id, ''), created_at, updated_at FROM identity WHERE singleton = 1`))
	if err == nil {
		if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
			return IdentityRecord{}, false, internal("identity initialization")
		}
		committed = true
		return existing, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return IdentityRecord{}, false, internal("identity initialization")
	}

	record, rollback, err := factory(ctx)
	if err != nil {
		return IdentityRecord{}, false, err
	}
	keep := false
	if rollback != nil {
		defer func() {
			if !keep {
				rollback()
			}
		}()
	}
	if !validIdentityRecord(record) {
		return IdentityRecord{}, false, ErrInvalid
	}
	_, err = conn.ExecContext(ctx, `INSERT INTO identity(singleton, algorithm, public_key, private_key_ref,
		owner_id, node_id, created_at, updated_at) VALUES (1, ?, ?, ?, NULL, NULL, ?, ?)`,
		record.Algorithm, record.PublicKey, record.PrivateKeyRef, timestamp(record.CreatedAt), timestamp(record.UpdatedAt))
	if err != nil {
		return IdentityRecord{}, false, internal("identity initialization")
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return IdentityRecord{}, false, internal("identity initialization")
	}
	committed = true
	keep = true
	return cloneIdentity(record), true, nil
}

func (s *Store) BindIdentity(ctx context.Context, ownerID, nodeID string) error {
	if err := requireContext(ctx); err != nil {
		return err
	}
	if ownerID == "" || nodeID == "" || len(ownerID) > 128 || len(nodeID) > 128 {
		return ErrInvalid
	}
	db, err := s.database()
	if err != nil {
		return err
	}
	result, err := db.ExecContext(ctx, `UPDATE identity SET owner_id = ?, node_id = ?, updated_at = ?
		WHERE singleton = 1 AND (owner_id IS NULL OR (owner_id = ? AND node_id = ?))`, ownerID, nodeID, timestamp(s.clock()), ownerID, nodeID)
	if err != nil {
		return internal("identity binding")
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return internal("identity binding")
	}
	if rows == 1 {
		return nil
	}
	var count int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM identity WHERE singleton = 1").Scan(&count); err != nil {
		return internal("identity binding")
	}
	if count == 0 {
		return ErrNotFound
	}
	return ErrConflict
}

type rowScanner interface{ Scan(...any) error }

func scanIdentity(row rowScanner) (IdentityRecord, error) {
	var record IdentityRecord
	var created, updated string
	if err := row.Scan(&record.Algorithm, &record.PublicKey, &record.PrivateKeyRef, &record.OwnerID, &record.NodeID, &created, &updated); err != nil {
		return IdentityRecord{}, err
	}
	var err error
	record.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return IdentityRecord{}, ErrCorrupt
	}
	record.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
	if err != nil || !validIdentityRecord(record) {
		return IdentityRecord{}, ErrCorrupt
	}
	return cloneIdentity(record), nil
}

func validIdentityRecord(record IdentityRecord) bool {
	return record.Algorithm == "ed25519" && len(record.PublicKey) == 32 && record.PrivateKeyRef != "" && len(record.PrivateKeyRef) <= 512 && !record.CreatedAt.IsZero() && !record.UpdatedAt.IsZero()
}

func cloneIdentity(record IdentityRecord) IdentityRecord {
	clone := record
	clone.PublicKey = append([]byte(nil), record.PublicKey...)
	return clone
}
