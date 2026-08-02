package store

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"time"

	"github.com/ncruces/go-sqlite3"
)

type BootstrapState string

const (
	BootstrapPending   BootstrapState = "pending"
	BootstrapCompleted BootstrapState = "completed"
)

type BootstrapStatus struct {
	State BootstrapState
}

type BootstrapClaim struct {
	SecretHash     []byte
	ClaimDigest    []byte
	OwnerID        string
	NodeID         string
	RequestID      string
	Name           string
	OS             string
	Version        string
	PublicKey      []byte
	CredentialHash []byte
	Now            time.Time
	RetryUntil     time.Time
}

type ClaimResult struct {
	OwnerID  string
	NodeID   string
	Replayed bool
}

type Owner struct {
	ID        string
	Status    string
	CreatedAt time.Time
}

type Node struct {
	ID             string
	OwnerID        string
	PublicKey      []byte
	Name           string
	OS             string
	Version        string
	Status         string
	CredentialHash []byte
	CreatedAt      time.Time
}

type ControlClient struct {
	ID        string
	OwnerID   string
	PublicKey []byte
	Name      string
	Status    string
	CreatedAt time.Time
}

type NodeSession struct {
	OwnerID        string
	NodeID         string
	PublicKey      []byte
	CredentialHash []byte
	Status         string
}

type ControlClientSession struct {
	OwnerID   string
	ClientID  string
	PublicKey []byte
	Status    string
}

func (s *Store) RotateBootstrap(ctx context.Context, secretHash []byte, now time.Time) (BootstrapStatus, error) {
	if err := requireContext(ctx); err != nil {
		return BootstrapStatus{}, err
	}
	if len(secretHash) != 32 || now.IsZero() {
		return BootstrapStatus{}, ErrInvalid
	}
	db, err := s.database()
	if err != nil {
		return BootstrapStatus{}, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return BootstrapStatus{}, internal("bootstrap rotation")
	}
	defer tx.Rollback()
	var owners int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM owners").Scan(&owners); err != nil {
		return BootstrapStatus{}, internal("bootstrap rotation")
	}
	if owners > 0 {
		var retryValue sql.NullString
		if err := tx.QueryRowContext(ctx, "SELECT retry_until FROM bootstrap WHERE singleton=1 AND status='completed'").Scan(&retryValue); err != nil {
			return BootstrapStatus{}, ErrCorrupt
		}
		if err := scrubExpiredBootstrap(ctx, tx, retryValue, now); err != nil {
			return BootstrapStatus{}, err
		}
		return BootstrapStatus{State: BootstrapCompleted}, tx.Commit()
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO bootstrap(singleton, status, secret_hash, issued_at)
		VALUES (1, 'pending', ?, ?)
		ON CONFLICT(singleton) DO UPDATE SET status='pending', secret_hash=excluded.secret_hash,
		claim_digest=NULL, owner_id=NULL, node_id=NULL, issued_at=excluded.issued_at,
		claimed_at=NULL, retry_until=NULL`, append([]byte(nil), secretHash...), timestamp(now)); err != nil {
		return BootstrapStatus{}, internal("bootstrap rotation")
	}
	if err := tx.Commit(); err != nil {
		return BootstrapStatus{}, internal("bootstrap rotation")
	}
	return BootstrapStatus{State: BootstrapPending}, nil
}

func (s *Store) BootstrapStatus(ctx context.Context) (BootstrapStatus, error) {
	if err := requireContext(ctx); err != nil {
		return BootstrapStatus{}, err
	}
	db, err := s.database()
	if err != nil {
		return BootstrapStatus{}, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return BootstrapStatus{}, internal("bootstrap status")
	}
	defer tx.Rollback()
	var state string
	var retryValue sql.NullString
	if err := tx.QueryRowContext(ctx, "SELECT status, retry_until FROM bootstrap WHERE singleton=1").Scan(&state, &retryValue); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return BootstrapStatus{}, ErrNotFound
		}
		return BootstrapStatus{}, internal("bootstrap status")
	}
	if state == string(BootstrapCompleted) {
		if err := scrubExpiredBootstrap(ctx, tx, retryValue, s.clock().UTC()); err != nil {
			return BootstrapStatus{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return BootstrapStatus{}, internal("bootstrap status")
	}
	return BootstrapStatus{State: BootstrapState(state)}, nil
}

func scrubExpiredBootstrap(ctx context.Context, tx *sql.Tx, retryValue sql.NullString, now time.Time) error {
	retryUntil, err := parseTimestamp(retryValue)
	if err != nil {
		return ErrCorrupt
	}
	if retryUntil.IsZero() || !now.After(retryUntil) {
		return nil
	}
	if _, err := tx.ExecContext(ctx, "UPDATE bootstrap SET secret_hash=NULL, claim_digest=NULL, retry_until=NULL WHERE singleton=1"); err != nil {
		return internal("bootstrap cleanup")
	}
	return nil
}

func (s *Store) ClaimBootstrap(ctx context.Context, claim BootstrapClaim) (ClaimResult, error) {
	if err := requireContext(ctx); err != nil {
		return ClaimResult{}, err
	}
	if err := validateClaim(claim); err != nil {
		return ClaimResult{}, err
	}
	db, err := s.database()
	if err != nil {
		return ClaimResult{}, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return ClaimResult{}, internal("bootstrap claim")
	}
	defer tx.Rollback()
	var state string
	var secretHash, claimDigest []byte
	var ownerID, nodeID sql.NullString
	var retryValue sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT status, secret_hash, claim_digest, owner_id, node_id, retry_until
		FROM bootstrap WHERE singleton=1`).Scan(&state, &secretHash, &claimDigest, &ownerID, &nodeID, &retryValue); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ClaimResult{}, ErrNotFound
		}
		return ClaimResult{}, internal("bootstrap claim")
	}
	if state == string(BootstrapCompleted) {
		retryUntil, parseErr := parseTimestamp(retryValue)
		if parseErr != nil {
			return ClaimResult{}, ErrCorrupt
		}
		if !retryUntil.IsZero() && !claim.Now.After(retryUntil) && equalDigest(secretHash, claim.SecretHash) && equalDigest(claimDigest, claim.ClaimDigest) {
			if err := tx.Commit(); err != nil {
				return ClaimResult{}, internal("bootstrap claim")
			}
			return ClaimResult{OwnerID: ownerID.String, NodeID: nodeID.String, Replayed: true}, nil
		}
		if !retryUntil.IsZero() && claim.Now.After(retryUntil) && len(secretHash) != 0 {
			if _, err := tx.ExecContext(ctx, "UPDATE bootstrap SET secret_hash=NULL, claim_digest=NULL, retry_until=NULL WHERE singleton=1"); err != nil {
				return ClaimResult{}, internal("bootstrap cleanup")
			}
		}
		if err := tx.Commit(); err != nil {
			return ClaimResult{}, internal("bootstrap claim")
		}
		return ClaimResult{}, ErrBootstrapCompleted
	}
	if state != string(BootstrapPending) || !equalDigest(secretHash, claim.SecretHash) {
		return ClaimResult{}, ErrUnauthorized
	}
	now := timestamp(claim.Now)
	if _, err := tx.ExecContext(ctx, "INSERT INTO owners(id, singleton, status, created_at) VALUES (?, 1, 'active', ?)", claim.OwnerID, now); err != nil {
		return ClaimResult{}, mapInsertError(ctx, err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO nodes(id, owner_id, public_key, name, os, version, status, created_at)
		VALUES (?, ?, ?, ?, ?, ?, 'active', ?)`, claim.NodeID, claim.OwnerID, append([]byte(nil), claim.PublicKey...), claim.Name, claim.OS, claim.Version, now); err != nil {
		return ClaimResult{}, mapInsertError(ctx, err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO node_credentials(node_id, credential_hash, status, created_at)
		VALUES (?, ?, 'active', ?)`, claim.NodeID, append([]byte(nil), claim.CredentialHash...), now); err != nil {
		return ClaimResult{}, mapInsertError(ctx, err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE bootstrap SET status='completed', claim_digest=?, owner_id=?, node_id=?,
		claimed_at=?, retry_until=? WHERE singleton=1 AND status='pending'`, append([]byte(nil), claim.ClaimDigest...), claim.OwnerID, claim.NodeID, now, timestamp(claim.RetryUntil)); err != nil {
		return ClaimResult{}, internal("bootstrap claim")
	}
	if err := tx.Commit(); err != nil {
		return ClaimResult{}, internal("bootstrap claim")
	}
	return ClaimResult{OwnerID: claim.OwnerID, NodeID: claim.NodeID}, nil
}

func validateClaim(claim BootstrapClaim) error {
	if len(claim.SecretHash) != 32 || len(claim.ClaimDigest) != 32 || len(claim.PublicKey) != 32 || len(claim.CredentialHash) != 32 ||
		claim.OwnerID == "" || claim.NodeID == "" || claim.RequestID == "" || claim.Name == "" || claim.OS == "" || claim.Version == "" ||
		claim.Now.IsZero() || !claim.RetryUntil.After(claim.Now) {
		return ErrInvalid
	}
	return nil
}

func equalDigest(left, right []byte) bool {
	return len(left) == 32 && len(right) == 32 && subtle.ConstantTimeCompare(left, right) == 1
}

func mapInsertError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	var sqliteError *sqlite3.Error
	if errors.As(err, &sqliteError) && sqliteError.Code() == sqlite3.CONSTRAINT {
		return ErrConflict
	}
	return internal("bootstrap claim")
}

func (s *Store) Owner(ctx context.Context) (Owner, error) {
	if err := requireContext(ctx); err != nil {
		return Owner{}, err
	}
	db, err := s.database()
	if err != nil {
		return Owner{}, err
	}
	var result Owner
	var created string
	if err := db.QueryRowContext(ctx, "SELECT id, status, created_at FROM owners LIMIT 1").Scan(&result.ID, &result.Status, &created); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Owner{}, ErrNotFound
		}
		return Owner{}, internal("owner read")
	}
	result.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return Owner{}, ErrCorrupt
	}
	return result, nil
}

func (s *Store) Nodes(ctx context.Context) ([]Node, error) {
	if err := requireContext(ctx); err != nil {
		return nil, err
	}
	db, err := s.database()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT n.id, n.owner_id, n.public_key, n.name, n.os, n.version, n.status,
		c.credential_hash, n.created_at FROM nodes n JOIN node_credentials c ON c.node_id=n.id ORDER BY n.id`)
	if err != nil {
		return nil, internal("nodes read")
	}
	defer rows.Close()
	var result []Node
	for rows.Next() {
		var item Node
		var created string
		if err := rows.Scan(&item.ID, &item.OwnerID, &item.PublicKey, &item.Name, &item.OS, &item.Version, &item.Status, &item.CredentialHash, &created); err != nil {
			return nil, internal("nodes read")
		}
		item.PublicKey = append([]byte(nil), item.PublicKey...)
		item.CredentialHash = append([]byte(nil), item.CredentialHash...)
		item.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
		if err != nil {
			return nil, ErrCorrupt
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, internal("nodes read")
	}
	return result, nil
}

func (s *Store) ControlClients(ctx context.Context) ([]ControlClient, error) {
	if err := requireContext(ctx); err != nil {
		return nil, err
	}
	db, err := s.database()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, "SELECT id, owner_id, public_key, name, status, created_at FROM control_clients ORDER BY id")
	if err != nil {
		return nil, internal("control clients read")
	}
	defer rows.Close()
	var result []ControlClient
	for rows.Next() {
		var item ControlClient
		var created string
		if err := rows.Scan(&item.ID, &item.OwnerID, &item.PublicKey, &item.Name, &item.Status, &created); err != nil {
			return nil, internal("control clients read")
		}
		item.PublicKey = append([]byte(nil), item.PublicKey...)
		item.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
		if err != nil {
			return nil, ErrCorrupt
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Store) NodeSession(ctx context.Context, nodeID string) (NodeSession, error) {
	if err := requireContext(ctx); err != nil {
		return NodeSession{}, err
	}
	if nodeID == "" {
		return NodeSession{}, ErrInvalid
	}
	db, err := s.database()
	if err != nil {
		return NodeSession{}, err
	}
	var result NodeSession
	if err := db.QueryRowContext(ctx, `SELECT n.owner_id, n.id, n.public_key, c.credential_hash,
		CASE WHEN n.status='active' AND c.status='active' THEN 'active' ELSE 'revoked' END
		FROM nodes n JOIN node_credentials c ON c.node_id=n.id WHERE n.id=?`, nodeID).
		Scan(&result.OwnerID, &result.NodeID, &result.PublicKey, &result.CredentialHash, &result.Status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return NodeSession{}, ErrNotFound
		}
		return NodeSession{}, internal("node session read")
	}
	result.PublicKey = append([]byte(nil), result.PublicKey...)
	result.CredentialHash = append([]byte(nil), result.CredentialHash...)
	return result, nil
}

func (s *Store) ControlClientSession(ctx context.Context, clientID string) (ControlClientSession, error) {
	if err := requireContext(ctx); err != nil {
		return ControlClientSession{}, err
	}
	if clientID == "" {
		return ControlClientSession{}, ErrInvalid
	}
	db, err := s.database()
	if err != nil {
		return ControlClientSession{}, err
	}
	var result ControlClientSession
	if err := db.QueryRowContext(ctx, "SELECT owner_id, id, public_key, status FROM control_clients WHERE id=?", clientID).
		Scan(&result.OwnerID, &result.ClientID, &result.PublicKey, &result.Status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ControlClientSession{}, ErrNotFound
		}
		return ControlClientSession{}, internal("control client session read")
	}
	result.PublicKey = append([]byte(nil), result.PublicKey...)
	return result, nil
}
