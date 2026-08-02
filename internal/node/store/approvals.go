package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

const (
	ApprovalPending   = "pending"
	ApprovalResolved  = "resolved"
	ApprovalAmbiguous = "ambiguous"
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
	_, err = db.ExecContext(ctx, `UPDATE approval_state SET status='ambiguous',updated_at=? WHERE thread_id=? AND status='pending'`, timestamp(s.clock()), threadID)
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
	return record.Status == ApprovalPending || record.Status == ApprovalResolved || record.Status == ApprovalAmbiguous
}

func nullTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return timestamp(value)
}
