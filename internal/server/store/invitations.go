package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type NodeInvitation struct {
	ID          string    `json:"invitationId"`
	OwnerID     string    `json:"-"`
	DisplayName string    `json:"displayName"`
	Status      string    `json:"status"`
	CreatedBy   string    `json:"createdBy"`
	CreatedAt   time.Time `json:"createdAt"`
	ExpiresAt   time.Time `json:"expiresAt"`
	UsedAt      time.Time `json:"usedAt,omitzero"`
	NodeID      string    `json:"nodeId,omitempty"`
}

type CreateNodeInvitation struct {
	NodeInvitation
	SecretHash []byte
	CodeHash   []byte
}

type ClaimNodeInvitation struct {
	InvitationID string
	ProofHash    []byte
	UseShortCode bool
	NodeID       string
	OwnerID      string
	PublicKey    []byte
	Name         string
	OS           string
	Arch         string
	Version      string
	Now          time.Time
}

func (s *Store) CreateNodeInvitation(ctx context.Context, value CreateNodeInvitation) error {
	if err := requireContext(ctx); err != nil {
		return err
	}
	if value.ID == "" || value.DisplayName == "" || value.CreatedBy == "" || len(value.SecretHash) != 32 || len(value.CodeHash) != 32 || value.CreatedAt.IsZero() || !value.ExpiresAt.After(value.CreatedAt) {
		return ErrInvalid
	}
	db, err := s.database()
	if err != nil {
		return err
	}
	var owner any
	if value.OwnerID != "" {
		owner = value.OwnerID
	}
	_, err = db.ExecContext(ctx, `INSERT INTO node_invitations(id,owner_id,secret_hash,code_hash,display_name,status,created_by,created_at,expires_at) VALUES(?,?,?,?,?,'pending',?,?,?)`, value.ID, owner, value.SecretHash, value.CodeHash, value.DisplayName, value.CreatedBy, timestamp(value.CreatedAt), timestamp(value.ExpiresAt))
	if err != nil {
		return mapInsertError(ctx, err)
	}
	return nil
}

func (s *Store) ListNodeInvitations(ctx context.Context, ownerID string, now time.Time) ([]NodeInvitation, error) {
	if err := requireContext(ctx); err != nil {
		return nil, err
	}
	if ownerID == "" || now.IsZero() {
		return nil, ErrInvalid
	}
	db, err := s.database()
	if err != nil {
		return nil, err
	}
	_, _ = db.ExecContext(ctx, `UPDATE node_invitations SET status='expired' WHERE owner_id=? AND status='pending' AND expires_at<=?`, ownerID, timestamp(now))
	rows, err := db.QueryContext(ctx, `SELECT id,owner_id,display_name,status,created_by,created_at,expires_at,COALESCE(used_at,''),COALESCE(node_id,'') FROM node_invitations WHERE owner_id=? ORDER BY created_at DESC LIMIT 100`, ownerID)
	if err != nil {
		return nil, internal("node invitations read")
	}
	defer rows.Close()
	var result []NodeInvitation
	for rows.Next() {
		item, scanErr := scanNodeInvitation(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, internal("node invitations read")
	}
	return result, nil
}

func (s *Store) CancelNodeInvitation(ctx context.Context, ownerID, id string, now time.Time) error {
	if err := requireContext(ctx); err != nil {
		return err
	}
	if ownerID == "" || id == "" || now.IsZero() {
		return ErrInvalid
	}
	db, err := s.database()
	if err != nil {
		return err
	}
	result, err := db.ExecContext(ctx, `UPDATE node_invitations SET status='cancelled' WHERE owner_id=? AND id=? AND status='pending'`, ownerID, id)
	if err != nil {
		return internal("node invitation cancel")
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return ErrConflict
	}
	return nil
}

func (s *Store) ClaimNodeInvitation(ctx context.Context, claim ClaimNodeInvitation) (NodeInvitation, error) {
	if err := requireContext(ctx); err != nil {
		return NodeInvitation{}, err
	}
	if len(claim.ProofHash) != 32 || claim.NodeID == "" || len(claim.PublicKey) != 32 || claim.Name == "" || claim.OS == "" || claim.Arch == "" || claim.Version == "" || claim.Now.IsZero() {
		return NodeInvitation{}, ErrInvalid
	}
	db, err := s.database()
	if err != nil {
		return NodeInvitation{}, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return NodeInvitation{}, internal("node invitation claim")
	}
	defer tx.Rollback()
	selector := "id=? AND secret_hash=?"
	args := []any{claim.InvitationID, claim.ProofHash}
	if claim.UseShortCode {
		selector, args = "code_hash=?", []any{claim.ProofHash}
	}
	row := tx.QueryRowContext(ctx, `SELECT id,COALESCE(owner_id,''),display_name,status,created_by,created_at,expires_at,COALESCE(used_at,''),COALESCE(node_id,'') FROM node_invitations WHERE `+selector, args...)
	item, err := scanNodeInvitation(row)
	if errors.Is(err, sql.ErrNoRows) {
		return NodeInvitation{}, ErrUnauthorized
	}
	if err != nil {
		return NodeInvitation{}, err
	}
	if item.Status != "pending" || !claim.Now.Before(item.ExpiresAt) {
		return NodeInvitation{}, ErrConflict
	}
	if item.OwnerID != "" {
		var enabled int
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT node_enrollment_enabled FROM server_security_settings WHERE owner_id=?),1)`, item.OwnerID).Scan(&enabled); err != nil {
			return NodeInvitation{}, internal("node invitation admission")
		}
		if enabled != 1 {
			return NodeInvitation{}, ErrConflict
		}
	}
	now := timestamp(claim.Now)
	initial := item.OwnerID == ""
	if item.OwnerID == "" {
		if claim.OwnerID == "" {
			return NodeInvitation{}, ErrInvalid
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO owners(id,singleton,status,created_at) VALUES(?,1,'active',?)`, claim.OwnerID, now); err != nil {
			return NodeInvitation{}, mapInsertError(ctx, err)
		}
		item.OwnerID = claim.OwnerID
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO nodes(id,owner_id,public_key,name,os,version,status,created_at) VALUES(?,?,?,?,?,?,'active',?)`, claim.NodeID, item.OwnerID, claim.PublicKey, claim.Name, claim.OS, claim.Version, now); err != nil {
		return NodeInvitation{}, mapInsertError(ctx, err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO node_credentials(node_id,credential_hash,status,created_at) VALUES(?,NULL,'active',?)`, claim.NodeID, now); err != nil {
		return NodeInvitation{}, mapInsertError(ctx, err)
	}
	if initial {
		if _, err := tx.ExecContext(ctx, `INSERT INTO bootstrap(singleton,status,secret_hash,claim_digest,owner_id,node_id,issued_at,claimed_at,retry_until) VALUES(1,'completed',NULL,NULL,?,?,?, ?,NULL)
			ON CONFLICT(singleton) DO UPDATE SET status='completed',secret_hash=NULL,claim_digest=NULL,owner_id=excluded.owner_id,node_id=excluded.node_id,claimed_at=excluded.claimed_at,retry_until=NULL`, item.OwnerID, claim.NodeID, now, now); err != nil {
			return NodeInvitation{}, internal("node invitation claim")
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE node_invitations SET status='used',used_at=?,node_id=?,secret_hash=randomblob(32),code_hash=randomblob(32) WHERE id=? AND status='pending'`, now, claim.NodeID, item.ID)
	if err != nil {
		return NodeInvitation{}, internal("node invitation claim")
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return NodeInvitation{}, ErrConflict
	}
	if err := tx.Commit(); err != nil {
		return NodeInvitation{}, internal("node invitation claim")
	}
	item.Status, item.UsedAt, item.NodeID = "used", claim.Now, claim.NodeID
	return item, nil
}

type rowScanner interface{ Scan(...any) error }

func scanNodeInvitation(row rowScanner) (NodeInvitation, error) {
	var item NodeInvitation
	var created, expires, used string
	if err := row.Scan(&item.ID, &item.OwnerID, &item.DisplayName, &item.Status, &item.CreatedBy, &created, &expires, &used, &item.NodeID); err != nil {
		return NodeInvitation{}, err
	}
	var err error
	if item.CreatedAt, err = time.Parse(time.RFC3339Nano, created); err != nil {
		return NodeInvitation{}, ErrCorrupt
	}
	if item.ExpiresAt, err = time.Parse(time.RFC3339Nano, expires); err != nil {
		return NodeInvitation{}, ErrCorrupt
	}
	if used != "" {
		if item.UsedAt, err = time.Parse(time.RFC3339Nano, used); err != nil {
			return NodeInvitation{}, ErrCorrupt
		}
	}
	return item, nil
}
