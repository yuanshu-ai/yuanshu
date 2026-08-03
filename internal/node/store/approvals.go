package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

const (
	ApprovalPending    = "pending"
	ApprovalProcessing = "processing"
	ApprovalAccepted   = "accepted"
	ApprovalDeclined   = "declined"
	ApprovalResolved   = "resolved"
	ApprovalExpired    = "expired"
	ApprovalAmbiguous  = "ambiguous"
)

type ApprovalRecord struct {
	ApprovalID      string
	WorkspaceID     string
	ThreadID        string
	TurnID          string
	ItemID          string
	Status          string
	OperationDigest string
	Payload         []byte
	ExpiresAt       time.Time
	UpdatedAt       time.Time
}

type ApprovalClaim struct {
	ApprovalID      string
	WorkspaceID     string
	ThreadID        string
	TurnID          string
	ItemID          string
	OperationDigest string
	Decision        string
	Now             time.Time
}

func (s *Store) SaveApproval(ctx context.Context, record ApprovalRecord) error {
	if err := requireContext(ctx); err != nil {
		return err
	}
	if !validApprovalRecord(record) {
		return ErrInvalid
	}
	db, err := s.database()
	if err != nil {
		return err
	}
	when := record.UpdatedAt
	if when.IsZero() {
		when = s.clock()
	}
	_, err = db.ExecContext(ctx, `INSERT INTO approval_state(approval_id,workspace_id,thread_id,turn_id,item_id,status,operation_digest,payload,expires_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?) ON CONFLICT(approval_id) DO UPDATE SET status=excluded.status,operation_digest=excluded.operation_digest,payload=excluded.payload,expires_at=excluded.expires_at,updated_at=excluded.updated_at`,
		record.ApprovalID, record.WorkspaceID, record.ThreadID, record.TurnID, record.ItemID, record.Status, nullText(record.OperationDigest), append([]byte(nil), record.Payload...), nullTime(record.ExpiresAt), timestamp(when))
	if err != nil {
		return internal("approval save")
	}
	return nil
}

func (s *Store) Approval(ctx context.Context, approvalID string) (ApprovalRecord, error) {
	if err := requireContext(ctx); err != nil {
		return ApprovalRecord{}, err
	}
	if !validWorkspaceText(approvalID, 128) {
		return ApprovalRecord{}, ErrInvalid
	}
	db, err := s.database()
	if err != nil {
		return ApprovalRecord{}, err
	}
	return scanApproval(db.QueryRowContext(ctx, `SELECT approval_id,workspace_id,thread_id,turn_id,item_id,status,operation_digest,payload,expires_at,updated_at FROM approval_state WHERE approval_id=?`, approvalID))
}

// ClaimApproval atomically moves a pending approval to processing. The
// transition is deliberately separate from runtime execution: once claimed,
// a crashed Node will reconcile it as ambiguous instead of executing a second
// approval after the browser retries.
func (s *Store) ClaimApproval(ctx context.Context, claim ApprovalClaim) (ApprovalRecord, error) {
	if err := requireContext(ctx); err != nil {
		return ApprovalRecord{}, err
	}
	if !validApprovalClaim(claim) {
		return ApprovalRecord{}, ErrInvalid
	}
	db, err := s.database()
	if err != nil {
		return ApprovalRecord{}, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return ApprovalRecord{}, internal("approval claim")
	}
	defer tx.Rollback()
	record, err := scanApproval(tx.QueryRowContext(ctx, `SELECT approval_id,workspace_id,thread_id,turn_id,item_id,status,operation_digest,payload,expires_at,updated_at FROM approval_state WHERE approval_id=?`, claim.ApprovalID))
	if err != nil {
		return ApprovalRecord{}, err
	}
	if record.Status != ApprovalPending || record.OperationDigest != claim.OperationDigest || record.WorkspaceID != claim.WorkspaceID || record.ThreadID != claim.ThreadID || record.TurnID != claim.TurnID || record.ItemID != claim.ItemID {
		return record, ErrConflict
	}
	if !record.ExpiresAt.IsZero() && !record.ExpiresAt.After(claim.Now) {
		record.Status = ApprovalExpired
		record.UpdatedAt = claim.Now.UTC()
		if _, err := tx.ExecContext(ctx, `UPDATE approval_state SET status=?,updated_at=? WHERE approval_id=? AND status=?`, ApprovalExpired, timestamp(record.UpdatedAt), claim.ApprovalID, ApprovalPending); err != nil {
			return ApprovalRecord{}, internal("approval expire")
		}
		if err := tx.Commit(); err != nil {
			return ApprovalRecord{}, internal("approval expire")
		}
		return record, ErrExpired
	}
	record.Status = ApprovalProcessing
	record.UpdatedAt = claim.Now.UTC()
	if _, err := tx.ExecContext(ctx, `UPDATE approval_state SET status=?,updated_at=? WHERE approval_id=? AND status=?`, ApprovalProcessing, timestamp(record.UpdatedAt), claim.ApprovalID, ApprovalPending); err != nil {
		return ApprovalRecord{}, internal("approval claim")
	}
	if err := tx.Commit(); err != nil {
		return ApprovalRecord{}, internal("approval claim")
	}
	return record, nil
}

func (s *Store) MarkApprovalAmbiguous(ctx context.Context, approvalID string) error {
	if err := requireContext(ctx); err != nil {
		return err
	}
	if !validWorkspaceText(approvalID, 128) {
		return ErrInvalid
	}
	db, err := s.database()
	if err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, `UPDATE approval_state SET status=?,updated_at=? WHERE approval_id=? AND status=?`, ApprovalAmbiguous, timestamp(s.clock()), approvalID, ApprovalProcessing); err != nil {
		return internal("approval reconcile")
	}
	return nil
}

func (s *Store) PendingApprovals(ctx context.Context, threadID string) ([]ApprovalRecord, error) {
	if err := requireContext(ctx); err != nil {
		return nil, err
	}
	if !validWorkspaceText(threadID, 128) {
		return nil, ErrInvalid
	}
	db, err := s.database()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT approval_id,workspace_id,thread_id,turn_id,item_id,status,operation_digest,payload,expires_at,updated_at FROM approval_state WHERE thread_id=? AND status='pending' ORDER BY updated_at,approval_id`, threadID)
	if err != nil {
		return nil, internal("approval list")
	}
	defer rows.Close()
	result := make([]ApprovalRecord, 0)
	for rows.Next() {
		record, err := scanApproval(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, record)
	}
	if rows.Err() != nil {
		return nil, internal("approval list")
	}
	return result, nil
}

func (s *Store) MarkThreadApprovalsAmbiguous(ctx context.Context, threadID string) error {
	if err := requireContext(ctx); err != nil {
		return err
	}
	if !validWorkspaceText(threadID, 128) {
		return ErrInvalid
	}
	db, err := s.database()
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `UPDATE approval_state SET status='ambiguous',updated_at=? WHERE thread_id=? AND status IN ('pending','processing')`, timestamp(s.clock()), threadID)
	if err != nil {
		return internal("approval reconcile")
	}
	return nil
}

type approvalScanner interface{ Scan(...any) error }

func scanApproval(scanner approvalScanner) (ApprovalRecord, error) {
	var record ApprovalRecord
	var digest, expires sql.NullString
	var updated string
	err := scanner.Scan(&record.ApprovalID, &record.WorkspaceID, &record.ThreadID, &record.TurnID, &record.ItemID, &record.Status, &digest, &record.Payload, &expires, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return ApprovalRecord{}, ErrNotFound
	}
	if err != nil {
		return ApprovalRecord{}, internal("approval read")
	}
	record.OperationDigest = digest.String
	if expires.Valid {
		record.ExpiresAt, err = time.Parse(time.RFC3339Nano, expires.String)
		if err != nil {
			return ApprovalRecord{}, ErrCorrupt
		}
	}
	record.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
	if err != nil {
		return ApprovalRecord{}, ErrCorrupt
	}
	record.Payload = append([]byte(nil), record.Payload...)
	return record, nil
}

func validApprovalRecord(record ApprovalRecord) bool {
	if !validWorkspaceText(record.ApprovalID, 128) || !validWorkspaceText(record.WorkspaceID, 128) || !validWorkspaceText(record.ThreadID, 128) || !validWorkspaceText(record.TurnID, 128) || !validWorkspaceText(record.ItemID, 128) || len(record.Payload) == 0 || len(record.Payload) > MaxSnapshotBytes {
		return false
	}
	if record.OperationDigest != "" && len(record.OperationDigest) != 43 {
		return false
	}
	return record.Status == ApprovalPending || record.Status == ApprovalProcessing || record.Status == ApprovalAccepted || record.Status == ApprovalDeclined || record.Status == ApprovalResolved || record.Status == ApprovalExpired || record.Status == ApprovalAmbiguous
}

func validApprovalClaim(claim ApprovalClaim) bool {
	return validWorkspaceText(claim.ApprovalID, 128) && validWorkspaceText(claim.WorkspaceID, 128) && validWorkspaceText(claim.ThreadID, 128) && validWorkspaceText(claim.TurnID, 128) && validWorkspaceText(claim.ItemID, 128) && len(claim.OperationDigest) == 43 && (claim.Decision == "accept" || claim.Decision == "decline") && !claim.Now.IsZero()
}

func nullTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return timestamp(value)
}
