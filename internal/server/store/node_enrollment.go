package store

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"time"
)

const MaxActiveNodes = 5

type NodeEnrollment struct {
	ID, OwnerID, IssuerNodeID, Status              string
	CandidateNodeID, Name, OS, Version             string
	CodeHash, PublicKey, CredentialHash, IssuerKey []byte
	Proof                                          []byte
	ExpiresAt, CreatedAt, ClaimedAt, ResolvedAt    time.Time
}

type NodeEnrollmentClaim struct {
	EnrollmentID, CandidateNodeID, Name, OS, Version string
	CodeHash, PublicKey, CredentialHash              []byte
	Now                                              time.Time
}

type NodeEnrollmentResolution struct {
	EnrollmentID, IssuerNodeID, Decision string
	Proof                                []byte
	Now                                  time.Time
}

type TrustManifest struct {
	Revision int64
	Clients  []ControlClient
}

func (s *Store) CreateNodeEnrollment(ctx context.Context, value NodeEnrollment) error {
	if err := requireContext(ctx); err != nil {
		return err
	}
	if value.ID == "" || value.OwnerID == "" || value.IssuerNodeID == "" || len(value.CodeHash) != 32 || value.CreatedAt.IsZero() || value.ExpiresAt.Sub(value.CreatedAt) <= 0 || value.ExpiresAt.Sub(value.CreatedAt) > 5*time.Minute {
		return ErrInvalid
	}
	db, err := s.database()
	if err != nil {
		return err
	}
	result, err := db.ExecContext(ctx, `INSERT INTO node_enrollments(id,owner_id,issuer_node_id,code_hash,status,expires_at,created_at)
		SELECT ?,?,?,?,'pending',?,? WHERE EXISTS(SELECT 1 FROM nodes WHERE id=? AND owner_id=? AND status='active')`,
		value.ID, value.OwnerID, value.IssuerNodeID, append([]byte(nil), value.CodeHash...), timestamp(value.ExpiresAt), timestamp(value.CreatedAt), value.IssuerNodeID, value.OwnerID)
	if err != nil {
		return pairingWriteError(ctx, err)
	}
	if rows, _ := result.RowsAffected(); rows != 1 {
		return ErrConflict
	}
	return nil
}

func (s *Store) IssuerNodeEnrollment(ctx context.Context, id, issuerNodeID string) (NodeEnrollment, error) {
	if err := requireContext(ctx); err != nil {
		return NodeEnrollment{}, err
	}
	if id == "" || issuerNodeID == "" {
		return NodeEnrollment{}, ErrInvalid
	}
	db, err := s.database()
	if err != nil {
		return NodeEnrollment{}, err
	}
	item, err := scanNodeEnrollment(db.QueryRowContext(ctx, nodeEnrollmentSelect+" WHERE e.id=? AND e.issuer_node_id=?", id, issuerNodeID))
	if err != nil {
		return NodeEnrollment{}, enrollmentReadError(err)
	}
	return item, nil
}

func (s *Store) ClaimNodeEnrollment(ctx context.Context, claim NodeEnrollmentClaim) (NodeEnrollment, error) {
	if err := requireContext(ctx); err != nil {
		return NodeEnrollment{}, err
	}
	if claim.EnrollmentID == "" || claim.CandidateNodeID == "" || claim.Name == "" || len(claim.Name) > 128 ||
		(claim.OS != "windows" && claim.OS != "linux" && claim.OS != "darwin") || claim.Version == "" || len(claim.Version) > 64 ||
		len(claim.CodeHash) != 32 || len(claim.PublicKey) != 32 || len(claim.CredentialHash) != 32 || claim.Now.IsZero() {
		return NodeEnrollment{}, ErrInvalid
	}
	db, err := s.database()
	if err != nil {
		return NodeEnrollment{}, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return NodeEnrollment{}, internal("node enrollment claim")
	}
	defer tx.Rollback()
	item, err := scanNodeEnrollment(tx.QueryRowContext(ctx, nodeEnrollmentSelect+" WHERE e.id=?", claim.EnrollmentID))
	if err != nil {
		return NodeEnrollment{}, enrollmentReadError(err)
	}
	if subtle.ConstantTimeCompare(item.CodeHash, claim.CodeHash) != 1 {
		return NodeEnrollment{}, ErrUnauthorized
	}
	if claim.Now.After(item.ExpiresAt) {
		if item.Status == "pending" || item.Status == "claimed" {
			_, _ = tx.ExecContext(ctx, "UPDATE node_enrollments SET status='expired',resolved_at=? WHERE id=?", timestamp(claim.Now), claim.EnrollmentID)
			_ = tx.Commit()
		}
		return NodeEnrollment{}, ErrConflict
	}
	if item.Status == "claimed" && item.CandidateNodeID == claim.CandidateNodeID && item.Name == claim.Name && item.OS == claim.OS && item.Version == claim.Version && subtle.ConstantTimeCompare(item.PublicKey, claim.PublicKey) == 1 && subtle.ConstantTimeCompare(item.CredentialHash, claim.CredentialHash) == 1 {
		if err := tx.Commit(); err != nil {
			return NodeEnrollment{}, internal("node enrollment claim")
		}
		return item, nil
	}
	if item.Status != "pending" {
		return NodeEnrollment{}, ErrConflict
	}
	result, err := tx.ExecContext(ctx, `UPDATE node_enrollments SET status='claimed',candidate_node_id=?,public_key=?,credential_hash=?,name=?,os=?,version=?,claimed_at=? WHERE id=? AND status='pending'`,
		claim.CandidateNodeID, append([]byte(nil), claim.PublicKey...), append([]byte(nil), claim.CredentialHash...), claim.Name, claim.OS, claim.Version, timestamp(claim.Now), claim.EnrollmentID)
	if err != nil {
		return NodeEnrollment{}, pairingWriteError(ctx, err)
	}
	if rows, _ := result.RowsAffected(); rows != 1 {
		return NodeEnrollment{}, ErrConflict
	}
	if err := tx.Commit(); err != nil {
		return NodeEnrollment{}, internal("node enrollment claim")
	}
	item.Status, item.CandidateNodeID, item.Name, item.OS, item.Version, item.ClaimedAt = "claimed", claim.CandidateNodeID, claim.Name, claim.OS, claim.Version, claim.Now
	item.PublicKey, item.CredentialHash = append([]byte(nil), claim.PublicKey...), append([]byte(nil), claim.CredentialHash...)
	return item, nil
}

func (s *Store) NodeEnrollment(ctx context.Context, id string, codeHash []byte, now time.Time) (NodeEnrollment, error) {
	if err := requireContext(ctx); err != nil {
		return NodeEnrollment{}, err
	}
	if id == "" || len(codeHash) != 32 || now.IsZero() {
		return NodeEnrollment{}, ErrInvalid
	}
	db, err := s.database()
	if err != nil {
		return NodeEnrollment{}, err
	}
	item, err := scanNodeEnrollment(db.QueryRowContext(ctx, nodeEnrollmentSelect+" WHERE e.id=?", id))
	if err != nil {
		return NodeEnrollment{}, enrollmentReadError(err)
	}
	if subtle.ConstantTimeCompare(item.CodeHash, codeHash) != 1 {
		return NodeEnrollment{}, ErrUnauthorized
	}
	if now.After(item.ExpiresAt) && (item.Status == "pending" || item.Status == "claimed") {
		_, _ = db.ExecContext(ctx, "UPDATE node_enrollments SET status='expired',resolved_at=? WHERE id=? AND status IN ('pending','claimed')", timestamp(now), id)
		item.Status, item.ResolvedAt = "expired", now
	}
	return cloneNodeEnrollment(item), nil
}

func (s *Store) PendingNodeEnrollments(ctx context.Context, issuerNodeID string, now time.Time) ([]NodeEnrollment, error) {
	if err := requireContext(ctx); err != nil {
		return nil, err
	}
	if issuerNodeID == "" || now.IsZero() {
		return nil, ErrInvalid
	}
	db, err := s.database()
	if err != nil {
		return nil, err
	}
	_, _ = db.ExecContext(ctx, "UPDATE node_enrollments SET status='expired',resolved_at=? WHERE issuer_node_id=? AND status IN ('pending','claimed') AND expires_at<?", timestamp(now), issuerNodeID, timestamp(now))
	rows, err := db.QueryContext(ctx, nodeEnrollmentSelect+" WHERE e.issuer_node_id=? AND e.status='claimed' ORDER BY e.claimed_at,e.id", issuerNodeID)
	if err != nil {
		return nil, internal("node enrollments read")
	}
	defer rows.Close()
	var result []NodeEnrollment
	for rows.Next() {
		item, scanErr := scanNodeEnrollment(rows)
		if scanErr != nil {
			return nil, enrollmentReadError(scanErr)
		}
		result = append(result, item)
	}
	if rows.Err() != nil {
		return nil, internal("node enrollments read")
	}
	return result, nil
}

func (s *Store) ResolveNodeEnrollment(ctx context.Context, value NodeEnrollmentResolution) (NodeEnrollment, error) {
	if err := requireContext(ctx); err != nil {
		return NodeEnrollment{}, err
	}
	if value.EnrollmentID == "" || value.IssuerNodeID == "" || (value.Decision != "accept" && value.Decision != "decline") || len(value.Proof) != 64 || value.Now.IsZero() {
		return NodeEnrollment{}, ErrInvalid
	}
	db, err := s.database()
	if err != nil {
		return NodeEnrollment{}, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return NodeEnrollment{}, internal("node enrollment decision")
	}
	defer tx.Rollback()
	item, err := scanNodeEnrollment(tx.QueryRowContext(ctx, nodeEnrollmentSelect+" WHERE e.id=? AND e.issuer_node_id=?", value.EnrollmentID, value.IssuerNodeID))
	if err != nil {
		return NodeEnrollment{}, enrollmentReadError(err)
	}
	if (item.Status == "approved" || item.Status == "declined") && subtle.ConstantTimeCompare(item.Proof, value.Proof) == 1 {
		if err := tx.Commit(); err != nil {
			return NodeEnrollment{}, internal("node enrollment decision")
		}
		return item, nil
	}
	if item.Status != "claimed" || value.Now.After(item.ExpiresAt) {
		return NodeEnrollment{}, ErrConflict
	}
	status := "declined"
	if value.Decision == "accept" {
		var count int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM nodes WHERE owner_id=? AND status='active'", item.OwnerID).Scan(&count); err != nil {
			return NodeEnrollment{}, internal("node enrollment decision")
		}
		if count >= MaxActiveNodes {
			return NodeEnrollment{}, ErrConflict
		}
		now := timestamp(value.Now)
		if _, err := tx.ExecContext(ctx, `INSERT INTO nodes(id,owner_id,public_key,name,os,version,status,created_at) VALUES(?,?,?,?,?,?,'active',?)`, item.CandidateNodeID, item.OwnerID, item.PublicKey, item.Name, item.OS, item.Version, now); err != nil {
			return NodeEnrollment{}, pairingWriteError(ctx, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO node_credentials(node_id,credential_hash,status,created_at) VALUES(?,?,'active',?)`, item.CandidateNodeID, item.CredentialHash, now); err != nil {
			return NodeEnrollment{}, pairingWriteError(ctx, err)
		}
		status = "approved"
	}
	result, err := tx.ExecContext(ctx, "UPDATE node_enrollments SET status=?,resolved_at=?,proof=? WHERE id=? AND status='claimed'", status, timestamp(value.Now), append([]byte(nil), value.Proof...), value.EnrollmentID)
	if err != nil {
		return NodeEnrollment{}, pairingWriteError(ctx, err)
	}
	if rows, _ := result.RowsAffected(); rows != 1 {
		return NodeEnrollment{}, ErrConflict
	}
	if err := tx.Commit(); err != nil {
		return NodeEnrollment{}, internal("node enrollment decision")
	}
	item.Status, item.Proof, item.ResolvedAt = status, append([]byte(nil), value.Proof...), value.Now
	return item, nil
}

func (s *Store) OwnerNodes(ctx context.Context, ownerID string) ([]Node, error) {
	if err := requireContext(ctx); err != nil {
		return nil, err
	}
	if ownerID == "" {
		return nil, ErrInvalid
	}
	db, err := s.database()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT id,owner_id,public_key,name,os,version,status,created_at,COALESCE(last_seen_at,'') FROM nodes WHERE owner_id=? ORDER BY created_at,id`, ownerID)
	if err != nil {
		return nil, internal("nodes read")
	}
	defer rows.Close()
	var result []Node
	for rows.Next() {
		var item Node
		var created, lastSeen string
		if err := rows.Scan(&item.ID, &item.OwnerID, &item.PublicKey, &item.Name, &item.OS, &item.Version, &item.Status, &created, &lastSeen); err != nil {
			return nil, internal("nodes read")
		}
		item.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
		if err != nil {
			return nil, ErrCorrupt
		}
		if lastSeen != "" {
			value, parseErr := time.Parse(time.RFC3339Nano, lastSeen)
			if parseErr != nil {
				return nil, ErrCorrupt
			}
			item.LastSeenAt = &value
		}
		item.PublicKey = append([]byte(nil), item.PublicKey...)
		result = append(result, item)
	}
	if rows.Err() != nil {
		return nil, internal("nodes read")
	}
	return result, nil
}

func (s *Store) RevokeNode(ctx context.Context, ownerID, issuerNodeID, targetNodeID string, now time.Time) error {
	if err := requireContext(ctx); err != nil {
		return err
	}
	if ownerID == "" || issuerNodeID == "" || targetNodeID == "" || issuerNodeID == targetNodeID || now.IsZero() {
		return ErrInvalid
	}
	db, err := s.database()
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return internal("node revoke")
	}
	defer tx.Rollback()
	var active int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM nodes WHERE owner_id=? AND status='active'", ownerID).Scan(&active); err != nil {
		return internal("node revoke")
	}
	if active <= 1 {
		return ErrConflict
	}
	var issuers int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM nodes WHERE id=? AND owner_id=? AND status='active'", issuerNodeID, ownerID).Scan(&issuers); err != nil || issuers != 1 {
		return ErrUnauthorized
	}
	result, err := tx.ExecContext(ctx, "UPDATE nodes SET status='revoked' WHERE id=? AND owner_id=? AND status='active'", targetNodeID, ownerID)
	if err != nil {
		return internal("node revoke")
	}
	if rows, _ := result.RowsAffected(); rows != 1 {
		return ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, "UPDATE node_credentials SET status='revoked',revoked_at=? WHERE node_id=?", timestamp(now), targetNodeID); err != nil {
		return internal("node revoke")
	}
	if _, err := tx.ExecContext(ctx, "UPDATE node_enrollments SET status='expired',resolved_at=? WHERE issuer_node_id=? AND status IN ('pending','claimed')", timestamp(now), targetNodeID); err != nil {
		return internal("node revoke")
	}
	if err := tx.Commit(); err != nil {
		return internal("node revoke")
	}
	return nil
}

func (s *Store) ControlTrustManifest(ctx context.Context, ownerID string) (TrustManifest, error) {
	if err := requireContext(ctx); err != nil {
		return TrustManifest{}, err
	}
	if ownerID == "" {
		return TrustManifest{}, ErrInvalid
	}
	db, err := s.database()
	if err != nil {
		return TrustManifest{}, err
	}
	var revision int64
	if err := db.QueryRowContext(ctx, "SELECT trust_revision FROM owners WHERE id=? AND status='active'", ownerID).Scan(&revision); err != nil {
		return TrustManifest{}, enrollmentReadError(err)
	}
	rows, err := db.QueryContext(ctx, "SELECT id,owner_id,key_id,public_key,name,status,created_at FROM control_clients WHERE owner_id=? ORDER BY id,key_id", ownerID)
	if err != nil {
		return TrustManifest{}, internal("control trust read")
	}
	defer rows.Close()
	result := TrustManifest{Revision: revision}
	for rows.Next() {
		var item ControlClient
		var created string
		if err := rows.Scan(&item.ID, &item.OwnerID, &item.KeyID, &item.PublicKey, &item.Name, &item.Status, &created); err != nil {
			return TrustManifest{}, internal("control trust read")
		}
		item.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
		if err != nil {
			return TrustManifest{}, ErrCorrupt
		}
		item.PublicKey = append([]byte(nil), item.PublicKey...)
		result.Clients = append(result.Clients, item)
	}
	if rows.Err() != nil {
		return TrustManifest{}, internal("control trust read")
	}
	return result, nil
}

const nodeEnrollmentSelect = `SELECT e.id,e.owner_id,e.issuer_node_id,e.code_hash,e.status,e.expires_at,COALESCE(e.candidate_node_id,''),COALESCE(e.public_key,X''),COALESCE(e.credential_hash,X''),COALESCE(e.name,''),COALESCE(e.os,''),COALESCE(e.version,''),e.created_at,COALESCE(e.claimed_at,''),COALESCE(e.resolved_at,''),COALESCE(e.proof,X''),n.public_key FROM node_enrollments e JOIN nodes n ON n.id=e.issuer_node_id`

func scanNodeEnrollment(row scanner) (NodeEnrollment, error) {
	var item NodeEnrollment
	var expires, created, claimed, resolved string
	if err := row.Scan(&item.ID, &item.OwnerID, &item.IssuerNodeID, &item.CodeHash, &item.Status, &expires, &item.CandidateNodeID, &item.PublicKey, &item.CredentialHash, &item.Name, &item.OS, &item.Version, &created, &claimed, &resolved, &item.Proof, &item.IssuerKey); err != nil {
		return NodeEnrollment{}, err
	}
	var err error
	item.ExpiresAt, err = time.Parse(time.RFC3339Nano, expires)
	if err != nil {
		return NodeEnrollment{}, ErrCorrupt
	}
	item.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return NodeEnrollment{}, ErrCorrupt
	}
	if claimed != "" {
		item.ClaimedAt, err = time.Parse(time.RFC3339Nano, claimed)
		if err != nil {
			return NodeEnrollment{}, ErrCorrupt
		}
	}
	if resolved != "" {
		item.ResolvedAt, err = time.Parse(time.RFC3339Nano, resolved)
		if err != nil {
			return NodeEnrollment{}, ErrCorrupt
		}
	}
	return cloneNodeEnrollment(item), nil
}
func cloneNodeEnrollment(v NodeEnrollment) NodeEnrollment {
	v.CodeHash = append([]byte(nil), v.CodeHash...)
	v.PublicKey = append([]byte(nil), v.PublicKey...)
	v.CredentialHash = append([]byte(nil), v.CredentialHash...)
	v.IssuerKey = append([]byte(nil), v.IssuerKey...)
	v.Proof = append([]byte(nil), v.Proof...)
	return v
}
func enrollmentReadError(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return internal("node enrollment read")
}
