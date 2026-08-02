package store

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"time"

	"github.com/ncruces/go-sqlite3"
)

type Pairing struct {
	ID, OwnerID, NodeID, Status     string
	CodeHash, Challenge             []byte
	ExpiresAt, CreatedAt            time.Time
	ClientID, KeyID, ClientName     string
	PublicKey, NodePublicKey, Proof []byte
	ClaimedAt, ResolvedAt           time.Time
}

type PairingClaim struct {
	PairingID, ClientID, KeyID, ClientName string
	CodeHash, PublicKey                    []byte
	Now                                    time.Time
}

type PairingResolution struct {
	PairingID, NodeID, Decision string
	Proof                       []byte
	Now                         time.Time
}

func (s *Store) CreatePairing(ctx context.Context, value Pairing) error {
	if err := requireContext(ctx); err != nil {
		return err
	}
	if !validPairing(value) {
		return ErrInvalid
	}
	db, err := s.database()
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `INSERT INTO pairings(id,owner_id,node_id,code_hash,challenge,status,expires_at,created_at)
		VALUES(?,?,?,?,?,'pending',?,?)`, value.ID, value.OwnerID, value.NodeID, append([]byte(nil), value.CodeHash...), append([]byte(nil), value.Challenge...), timestamp(value.ExpiresAt), timestamp(value.CreatedAt))
	if err != nil {
		return pairingWriteError(ctx, err)
	}
	return nil
}

func (s *Store) ClaimPairing(ctx context.Context, claim PairingClaim) (Pairing, error) {
	if err := requireContext(ctx); err != nil {
		return Pairing{}, err
	}
	if !validPairingClaim(claim) {
		return Pairing{}, ErrInvalid
	}
	db, err := s.database()
	if err != nil {
		return Pairing{}, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return Pairing{}, internal("pairing claim")
	}
	defer tx.Rollback()
	item, err := scanPairing(tx.QueryRowContext(ctx, pairingSelect+" WHERE p.id=?", claim.PairingID))
	if err != nil {
		return Pairing{}, pairingReadError(err)
	}
	if subtle.ConstantTimeCompare(item.CodeHash, claim.CodeHash) != 1 {
		return Pairing{}, ErrUnauthorized
	}
	if claim.Now.After(item.ExpiresAt) {
		if item.Status == "pending" || item.Status == "claimed" {
			_, _ = tx.ExecContext(ctx, "UPDATE pairings SET status='expired',resolved_at=? WHERE id=?", timestamp(claim.Now), claim.PairingID)
			_ = tx.Commit()
		}
		return Pairing{}, ErrConflict
	}
	if item.Status == "claimed" && item.ClientID == claim.ClientID && item.KeyID == claim.KeyID && item.ClientName == claim.ClientName && subtle.ConstantTimeCompare(item.PublicKey, claim.PublicKey) == 1 {
		if err := tx.Commit(); err != nil {
			return Pairing{}, internal("pairing claim")
		}
		return item, nil
	}
	if item.Status != "pending" {
		return Pairing{}, ErrConflict
	}
	_, err = tx.ExecContext(ctx, `UPDATE pairings SET status='claimed',client_id=?,key_id=?,public_key=?,client_name=?,claimed_at=? WHERE id=? AND status='pending'`,
		claim.ClientID, claim.KeyID, append([]byte(nil), claim.PublicKey...), claim.ClientName, timestamp(claim.Now), claim.PairingID)
	if err != nil {
		return Pairing{}, pairingWriteError(ctx, err)
	}
	if err := tx.Commit(); err != nil {
		return Pairing{}, internal("pairing claim")
	}
	item.Status, item.ClientID, item.KeyID, item.ClientName, item.PublicKey, item.ClaimedAt = "claimed", claim.ClientID, claim.KeyID, claim.ClientName, append([]byte(nil), claim.PublicKey...), claim.Now
	return item, nil
}

func (s *Store) Pairing(ctx context.Context, pairingID string, codeHash []byte, now time.Time) (Pairing, error) {
	if err := requireContext(ctx); err != nil {
		return Pairing{}, err
	}
	if pairingID == "" || len(pairingID) > 128 || len(codeHash) != 32 || now.IsZero() {
		return Pairing{}, ErrInvalid
	}
	db, err := s.database()
	if err != nil {
		return Pairing{}, err
	}
	item, err := scanPairing(db.QueryRowContext(ctx, pairingSelect+" WHERE p.id=?", pairingID))
	if err != nil {
		return Pairing{}, pairingReadError(err)
	}
	if subtle.ConstantTimeCompare(item.CodeHash, codeHash) != 1 {
		return Pairing{}, ErrUnauthorized
	}
	if now.After(item.ExpiresAt) && item.Status != "pending" && item.Status != "claimed" {
		return Pairing{}, ErrConflict
	}
	if now.After(item.ExpiresAt) && (item.Status == "pending" || item.Status == "claimed") {
		_, _ = db.ExecContext(ctx, "UPDATE pairings SET status='expired',resolved_at=? WHERE id=? AND status IN ('pending','claimed')", timestamp(now), pairingID)
		item.Status, item.ResolvedAt = "expired", now
	}
	return clonePairing(item), nil
}

func (s *Store) NodePairings(ctx context.Context, nodeID string, now time.Time) ([]Pairing, error) {
	if err := requireContext(ctx); err != nil {
		return nil, err
	}
	if nodeID == "" || len(nodeID) > 128 || now.IsZero() {
		return nil, ErrInvalid
	}
	db, err := s.database()
	if err != nil {
		return nil, err
	}
	_, _ = db.ExecContext(ctx, "UPDATE pairings SET status='expired',resolved_at=? WHERE node_id=? AND status IN ('pending','claimed') AND expires_at<?", timestamp(now), nodeID, timestamp(now))
	rows, err := db.QueryContext(ctx, pairingSelect+" WHERE p.node_id=? AND p.status='claimed' ORDER BY p.claimed_at,p.id", nodeID)
	if err != nil {
		return nil, internal("pairings read")
	}
	defer rows.Close()
	var result []Pairing
	for rows.Next() {
		item, scanErr := scanPairing(rows)
		if scanErr != nil {
			return nil, pairingReadError(scanErr)
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, internal("pairings read")
	}
	return result, nil
}

func (s *Store) ResolvePairing(ctx context.Context, value PairingResolution) (Pairing, error) {
	if err := requireContext(ctx); err != nil {
		return Pairing{}, err
	}
	if value.PairingID == "" || value.NodeID == "" || (value.Decision != "accept" && value.Decision != "decline") || len(value.Proof) != 64 || value.Now.IsZero() {
		return Pairing{}, ErrInvalid
	}
	db, err := s.database()
	if err != nil {
		return Pairing{}, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return Pairing{}, internal("pairing decision")
	}
	defer tx.Rollback()
	item, err := scanPairing(tx.QueryRowContext(ctx, pairingSelect+" WHERE p.id=? AND p.node_id=?", value.PairingID, value.NodeID))
	if err != nil {
		return Pairing{}, pairingReadError(err)
	}
	if item.Status != "claimed" || value.Now.After(item.ExpiresAt) {
		return Pairing{}, ErrConflict
	}
	status := "declined"
	if value.Decision == "accept" {
		status = "approved"
		_, err = tx.ExecContext(ctx, `INSERT INTO control_clients(id,owner_id,public_key,name,status,created_at,key_id) VALUES(?,?,?,?,'active',?,?)`, item.ClientID, item.OwnerID, item.PublicKey, item.ClientName, timestamp(value.Now), item.KeyID)
		if err != nil {
			return Pairing{}, pairingWriteError(ctx, err)
		}
	}
	_, err = tx.ExecContext(ctx, "UPDATE pairings SET status=?,resolved_at=?,proof=? WHERE id=? AND status='claimed'", status, timestamp(value.Now), append([]byte(nil), value.Proof...), value.PairingID)
	if err != nil {
		return Pairing{}, pairingWriteError(ctx, err)
	}
	if err := tx.Commit(); err != nil {
		return Pairing{}, internal("pairing decision")
	}
	item.Status, item.Proof, item.ResolvedAt = status, append([]byte(nil), value.Proof...), value.Now
	return item, nil
}

func (s *Store) RevokeControlClient(ctx context.Context, ownerID, clientID string, now time.Time) error {
	if err := requireContext(ctx); err != nil {
		return err
	}
	if ownerID == "" || clientID == "" || now.IsZero() {
		return ErrInvalid
	}
	db, err := s.database()
	if err != nil {
		return err
	}
	result, err := db.ExecContext(ctx, "UPDATE control_clients SET status='revoked',revoked_at=? WHERE owner_id=? AND id=? AND status='active'", timestamp(now), ownerID, clientID)
	if err != nil {
		return internal("control client revoke")
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) RotateNodeCredential(ctx context.Context, ownerID, nodeID string, credentialHash []byte, now time.Time) error {
	if err := requireContext(ctx); err != nil {
		return err
	}
	if ownerID == "" || nodeID == "" || len(credentialHash) != 32 || now.IsZero() {
		return ErrInvalid
	}
	db, err := s.database()
	if err != nil {
		return err
	}
	result, err := db.ExecContext(ctx, `UPDATE node_credentials SET credential_hash=?,status='active',rotated_at=?,revoked_at=NULL
		WHERE node_id=? AND EXISTS(SELECT 1 FROM nodes WHERE id=? AND owner_id=? AND status='active')`, append([]byte(nil), credentialHash...), timestamp(now), nodeID, nodeID, ownerID)
	if err != nil {
		return pairingWriteError(ctx, err)
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		return ErrNotFound
	}
	return nil
}

const pairingSelect = `SELECT p.id,p.owner_id,p.node_id,p.code_hash,p.challenge,p.status,p.expires_at,
	COALESCE(p.client_id,''),COALESCE(p.key_id,''),COALESCE(p.public_key,X''),COALESCE(p.client_name,''),p.created_at,
	COALESCE(p.claimed_at,''),COALESCE(p.resolved_at,''),COALESCE(p.proof,X''),n.public_key
	FROM pairings p JOIN nodes n ON n.id=p.node_id`

type scanner interface{ Scan(...any) error }

func scanPairing(row scanner) (Pairing, error) {
	var item Pairing
	var expires, created, claimed, resolved string
	if err := row.Scan(&item.ID, &item.OwnerID, &item.NodeID, &item.CodeHash, &item.Challenge, &item.Status, &expires, &item.ClientID, &item.KeyID, &item.PublicKey, &item.ClientName, &created, &claimed, &resolved, &item.Proof, &item.NodePublicKey); err != nil {
		return Pairing{}, err
	}
	var err error
	item.ExpiresAt, err = time.Parse(time.RFC3339Nano, expires)
	if err != nil {
		return Pairing{}, ErrCorrupt
	}
	item.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return Pairing{}, ErrCorrupt
	}
	if claimed != "" {
		item.ClaimedAt, err = time.Parse(time.RFC3339Nano, claimed)
		if err != nil {
			return Pairing{}, ErrCorrupt
		}
	}
	if resolved != "" {
		item.ResolvedAt, err = time.Parse(time.RFC3339Nano, resolved)
		if err != nil {
			return Pairing{}, ErrCorrupt
		}
	}
	item.CodeHash = append([]byte(nil), item.CodeHash...)
	item.Challenge = append([]byte(nil), item.Challenge...)
	item.PublicKey = append([]byte(nil), item.PublicKey...)
	item.NodePublicKey = append([]byte(nil), item.NodePublicKey...)
	item.Proof = append([]byte(nil), item.Proof...)
	return item, nil
}

func validPairing(value Pairing) bool {
	return value.ID != "" && len(value.ID) <= 128 && value.OwnerID != "" && value.NodeID != "" && len(value.CodeHash) == 32 && len(value.Challenge) == 32 && !value.CreatedAt.IsZero() && value.ExpiresAt.After(value.CreatedAt) && value.ExpiresAt.Sub(value.CreatedAt) <= 5*time.Minute
}
func validPairingClaim(value PairingClaim) bool {
	return value.PairingID != "" && value.ClientID != "" && value.KeyID != "" && value.ClientName != "" && len(value.ClientName) <= 128 && len(value.CodeHash) == 32 && len(value.PublicKey) == 32 && !value.Now.IsZero()
}
func clonePairing(value Pairing) Pairing {
	value.CodeHash = append([]byte(nil), value.CodeHash...)
	value.Challenge = append([]byte(nil), value.Challenge...)
	value.PublicKey = append([]byte(nil), value.PublicKey...)
	value.NodePublicKey = append([]byte(nil), value.NodePublicKey...)
	value.Proof = append([]byte(nil), value.Proof...)
	return value
}
func pairingReadError(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return internal("pairing read")
}
func pairingWriteError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var sqliteErr *sqlite3.Error
	if errors.As(err, &sqliteErr) && sqliteErr.Code() == sqlite3.CONSTRAINT {
		return ErrConflict
	}
	return internal("pairing write")
}
