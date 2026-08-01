package store

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"errors"
	"time"

	v1 "github.com/yuanshu-ai/yuanshu/internal/protocol/v1"
)

var (
	_ v1.TrustStore  = (*Store)(nil)
	_ v1.ReplayStore = (*Store)(nil)
)

func (s *Store) PutTrustedKey(ctx context.Context, ref v1.KeyRef, key v1.TrustedKey) error {
	if err := requireContext(ctx); err != nil {
		return err
	}
	if !validKeyRef(ref) || len(key.PublicKey) != ed25519.PublicKeySize || (key.Status != v1.TrustStatusActive && key.Status != v1.TrustStatusRevoked) {
		return ErrInvalid
	}
	db, err := s.database()
	if err != nil {
		return err
	}
	now := timestamp(s.clock())
	var revoked any
	if key.Status == v1.TrustStatusRevoked {
		revoked = now
	}
	_, err = db.ExecContext(ctx, `INSERT INTO trusted_clients(owner_id, node_id, client_id, key_id, public_key, status, created_at, revoked_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(owner_id, node_id, client_id, key_id) DO UPDATE SET
		public_key = excluded.public_key, status = excluded.status, revoked_at = excluded.revoked_at`,
		ref.OwnerID, ref.NodeID, ref.ClientID, ref.KeyID, key.PublicKey, key.Status, now, revoked)
	if err != nil {
		return internal("trusted key write")
	}
	return nil
}

func (s *Store) RevokeTrustedKey(ctx context.Context, ref v1.KeyRef) error {
	if err := requireContext(ctx); err != nil {
		return err
	}
	if !validKeyRef(ref) {
		return ErrInvalid
	}
	db, err := s.database()
	if err != nil {
		return err
	}
	result, err := db.ExecContext(ctx, `UPDATE trusted_clients SET status = 'revoked', revoked_at = ?
		WHERE owner_id = ? AND node_id = ? AND client_id = ? AND key_id = ?`,
		timestamp(s.clock()), ref.OwnerID, ref.NodeID, ref.ClientID, ref.KeyID)
	if err != nil {
		return internal("trusted key revoke")
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return v1.ErrTrustKeyNotFound
	}
	return nil
}

func (s *Store) LookupControlKey(ctx context.Context, ref v1.KeyRef) (v1.TrustedKey, error) {
	if err := requireContext(ctx); err != nil {
		return v1.TrustedKey{}, err
	}
	if !validKeyRef(ref) {
		return v1.TrustedKey{}, ErrInvalid
	}
	db, err := s.database()
	if err != nil {
		return v1.TrustedKey{}, err
	}
	var key v1.TrustedKey
	err = db.QueryRowContext(ctx, `SELECT public_key, status FROM trusted_clients
		WHERE owner_id = ? AND node_id = ? AND client_id = ? AND key_id = ?`,
		ref.OwnerID, ref.NodeID, ref.ClientID, ref.KeyID).Scan(&key.PublicKey, &key.Status)
	if errors.Is(err, sql.ErrNoRows) {
		return v1.TrustedKey{}, v1.ErrTrustKeyNotFound
	}
	if err != nil || len(key.PublicKey) != ed25519.PublicKeySize {
		return v1.TrustedKey{}, internal("trusted key read")
	}
	key.PublicKey = append([]byte(nil), key.PublicKey...)
	return key, nil
}

func (s *Store) CheckAndRecord(ctx context.Context, record v1.ReplayRecord) error {
	if err := requireContext(ctx); err != nil {
		return err
	}
	if !validReplayRecord(record) {
		return ErrInvalid
	}
	db, err := s.database()
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return internal("replay transaction")
	}
	defer tx.Rollback()
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM replay_messages WHERE owner_id = ? AND node_id = ? AND message_id = ?)`, record.OwnerID, record.NodeID, record.MessageID).Scan(&exists); err != nil {
		return internal("replay check")
	}
	if exists != 0 {
		return v1.ErrReplayDetected
	}
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM replay_nonces WHERE owner_id = ? AND node_id = ? AND client_id = ? AND key_id = ? AND nonce = ?)`, record.OwnerID, record.NodeID, record.ClientID, record.KeyID, record.Nonce).Scan(&exists); err != nil {
		return internal("replay check")
	}
	if exists != 0 {
		return v1.ErrReplayDetected
	}
	var highest int64
	err = tx.QueryRowContext(ctx, `SELECT highest_sequence FROM signer_sequences WHERE owner_id = ? AND node_id = ? AND client_id = ? AND key_id = ?`, record.OwnerID, record.NodeID, record.ClientID, record.KeyID).Scan(&highest)
	if err == nil && record.Sequence <= highest {
		return v1.ErrReplayDetected
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return internal("replay check")
	}
	now := timestamp(s.clock())
	if _, err := tx.ExecContext(ctx, `INSERT INTO replay_messages(owner_id, node_id, message_id, accepted_at) VALUES (?, ?, ?, ?)`, record.OwnerID, record.NodeID, record.MessageID, now); err != nil {
		return v1.ErrReplayDetected
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO replay_nonces(owner_id, node_id, client_id, key_id, nonce, retain_until) VALUES (?, ?, ?, ?, ?, ?)`, record.OwnerID, record.NodeID, record.ClientID, record.KeyID, record.Nonce, record.NonceRetainTo.UnixNano()); err != nil {
		return v1.ErrReplayDetected
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO signer_sequences(owner_id, node_id, client_id, key_id, highest_sequence, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(owner_id, node_id, client_id, key_id) DO UPDATE SET highest_sequence = excluded.highest_sequence, updated_at = excluded.updated_at`,
		record.OwnerID, record.NodeID, record.ClientID, record.KeyID, record.Sequence, now); err != nil {
		return internal("replay record")
	}
	if err := tx.Commit(); err != nil {
		return internal("replay record")
	}
	return nil
}

func (s *Store) PruneExpiredNonces(ctx context.Context, before time.Time) (int64, error) {
	if err := requireContext(ctx); err != nil {
		return 0, err
	}
	db, err := s.database()
	if err != nil {
		return 0, err
	}
	result, err := db.ExecContext(ctx, "DELETE FROM replay_nonces WHERE retain_until < ?", before.UnixNano())
	if err != nil {
		return 0, internal("nonce pruning")
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, internal("nonce pruning")
	}
	return count, nil
}

func validKeyRef(ref v1.KeyRef) bool {
	return ref.OwnerID != "" && ref.NodeID != "" && ref.ClientID != "" && ref.KeyID != "" && len(ref.OwnerID) <= 128 && len(ref.NodeID) <= 128 && len(ref.ClientID) <= 128 && len(ref.KeyID) <= 128
}

func validReplayRecord(record v1.ReplayRecord) bool {
	return validKeyRef(v1.KeyRef{OwnerID: record.OwnerID, NodeID: record.NodeID, ClientID: record.ClientID, KeyID: record.KeyID}) && record.MessageID != "" && len(record.MessageID) <= 128 && record.Nonce != "" && len(record.Nonce) <= 128 && record.Sequence >= 0 && record.Sequence <= 9007199254740991 && !record.NonceRetainTo.IsZero()
}
