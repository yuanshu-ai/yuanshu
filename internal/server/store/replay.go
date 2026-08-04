package store

import (
	"context"
	"database/sql"
	"errors"

	protocolv1 "github.com/yuanshu-ai/yuanshu/internal/protocol/v1"
)

var _ protocolv1.ReplayStore = (*Store)(nil)

// CheckAndRecord atomically consumes a signed control envelope at the Server
// boundary. The Node performs the same check independently; keeping both
// stores prevents Server-handled controls from becoming a weaker path.
func (s *Store) CheckAndRecord(ctx context.Context, record protocolv1.ReplayRecord) error {
	if err := requireContext(ctx); err != nil {
		return err
	}
	if !validServerReplayRecord(record) {
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

	now := s.clock().UTC()
	if _, err := tx.ExecContext(ctx, "DELETE FROM server_replay_messages WHERE retain_until < ?", now.UnixNano()); err != nil {
		return internal("replay prune")
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM server_replay_nonces WHERE retain_until < ?", now.UnixNano()); err != nil {
		return internal("replay prune")
	}

	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM server_replay_messages
		WHERE owner_id=? AND node_id=? AND client_id=? AND key_id=? AND message_id=?)`,
		record.OwnerID, record.NodeID, record.ClientID, record.KeyID, record.MessageID).Scan(&exists); err != nil {
		return internal("replay check")
	}
	if exists != 0 {
		return protocolv1.ErrReplayDetected
	}
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM server_replay_nonces
		WHERE owner_id=? AND node_id=? AND client_id=? AND key_id=? AND nonce=?)`,
		record.OwnerID, record.NodeID, record.ClientID, record.KeyID, record.Nonce).Scan(&exists); err != nil {
		return internal("replay check")
	}
	if exists != 0 {
		return protocolv1.ErrReplayDetected
	}

	var highest int64
	err = tx.QueryRowContext(ctx, `SELECT highest_sequence FROM server_signer_sequences
		WHERE owner_id=? AND node_id=? AND client_id=? AND key_id=?`,
		record.OwnerID, record.NodeID, record.ClientID, record.KeyID).Scan(&highest)
	if err == nil && record.Sequence <= highest {
		return protocolv1.ErrReplayDetected
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return internal("replay check")
	}

	acceptedAt := timestamp(now)
	retainUntil := record.NonceRetainTo.UTC().UnixNano()
	if _, err := tx.ExecContext(ctx, `INSERT INTO server_replay_messages
		(owner_id,node_id,client_id,key_id,message_id,retain_until,accepted_at) VALUES(?,?,?,?,?,?,?)`,
		record.OwnerID, record.NodeID, record.ClientID, record.KeyID, record.MessageID, retainUntil, acceptedAt); err != nil {
		return protocolv1.ErrReplayDetected
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO server_replay_nonces
		(owner_id,node_id,client_id,key_id,nonce,retain_until) VALUES(?,?,?,?,?,?)`,
		record.OwnerID, record.NodeID, record.ClientID, record.KeyID, record.Nonce, retainUntil); err != nil {
		return protocolv1.ErrReplayDetected
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO server_signer_sequences
		(owner_id,node_id,client_id,key_id,highest_sequence,updated_at) VALUES(?,?,?,?,?,?)
		ON CONFLICT(owner_id,node_id,client_id,key_id) DO UPDATE SET
			highest_sequence=excluded.highest_sequence,updated_at=excluded.updated_at`,
		record.OwnerID, record.NodeID, record.ClientID, record.KeyID, record.Sequence, acceptedAt); err != nil {
		return internal("replay record")
	}
	if err := tx.Commit(); err != nil {
		return internal("replay record")
	}
	return nil
}

func validServerReplayRecord(record protocolv1.ReplayRecord) bool {
	return record.OwnerID != "" && len(record.OwnerID) <= 128 &&
		record.NodeID != "" && len(record.NodeID) <= 128 &&
		record.ClientID != "" && len(record.ClientID) <= 128 &&
		record.KeyID != "" && len(record.KeyID) <= 128 &&
		record.MessageID != "" && len(record.MessageID) <= 128 &&
		record.Nonce != "" && len(record.Nonce) <= 128 &&
		record.Sequence >= 1 && record.Sequence <= 9007199254740991 &&
		!record.NonceRetainTo.IsZero()
}
