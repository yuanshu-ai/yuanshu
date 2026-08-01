package store

import (
	"context"
	"database/sql"
)

const (
	RuntimeThreadIdle           = "idle"
	RuntimeThreadStarting       = "starting"
	RuntimeThreadActive         = "active"
	RuntimeThreadNeedsReconcile = "needs_reconcile"
)

type RuntimeThreadRecord struct {
	Adapter      string
	ThreadID     string
	WorkspaceID  string
	Ownership    string
	State        string
	ActiveTurnID string
}

func (s *Store) SaveRuntimeThread(ctx context.Context, record RuntimeThreadRecord) error {
	if err := requireContext(ctx); err != nil {
		return err
	}
	if !validRuntimeThread(record) {
		return ErrInvalid
	}
	db, err := s.database()
	if err != nil {
		return err
	}
	var activeTurn any
	if record.ActiveTurnID != "" {
		activeTurn = record.ActiveTurnID
	}
	_, err = db.ExecContext(ctx, `INSERT INTO runtime_threads(
		adapter, thread_id, workspace_id, ownership, state, active_turn_id, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(thread_id) DO UPDATE SET
		adapter=excluded.adapter,
		workspace_id=excluded.workspace_id,
		ownership=excluded.ownership,
		state=excluded.state,
		active_turn_id=excluded.active_turn_id,
		updated_at=excluded.updated_at`,
		record.Adapter, record.ThreadID, record.WorkspaceID, record.Ownership,
		record.State, activeTurn, timestamp(s.clock().UTC()))
	if err != nil {
		return internal("runtime thread save")
	}
	return nil
}

func (s *Store) RuntimeThread(ctx context.Context, threadID string) (RuntimeThreadRecord, error) {
	if err := requireContext(ctx); err != nil {
		return RuntimeThreadRecord{}, err
	}
	if !validWorkspaceText(threadID, 128) {
		return RuntimeThreadRecord{}, ErrInvalid
	}
	db, err := s.database()
	if err != nil {
		return RuntimeThreadRecord{}, err
	}
	return scanRuntimeThread(db.QueryRowContext(ctx, `SELECT adapter, thread_id, workspace_id, ownership, state, active_turn_id
		FROM runtime_threads WHERE thread_id = ?`, threadID))
}

func (s *Store) RuntimeThreads(ctx context.Context) ([]RuntimeThreadRecord, error) {
	if err := requireContext(ctx); err != nil {
		return nil, err
	}
	db, err := s.database()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT adapter, thread_id, workspace_id, ownership, state, active_turn_id
		FROM runtime_threads ORDER BY thread_id`)
	if err != nil {
		return nil, internal("runtime thread list")
	}
	defer rows.Close()
	records := make([]RuntimeThreadRecord, 0)
	for rows.Next() {
		record, err := scanRuntimeThread(rows)
		if err != nil {
			return nil, internal("runtime thread list")
		}
		records = append(records, record)
	}
	if rows.Err() != nil {
		return nil, internal("runtime thread list")
	}
	return records, nil
}

func (s *Store) DeleteRuntimeThread(ctx context.Context, threadID string) error {
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
	result, err := db.ExecContext(ctx, "DELETE FROM runtime_threads WHERE thread_id = ?", threadID)
	if err != nil {
		return internal("runtime thread delete")
	}
	count, err := result.RowsAffected()
	if err != nil {
		return internal("runtime thread delete")
	}
	if count == 0 {
		return ErrNotFound
	}
	return nil
}

type runtimeThreadScanner interface{ Scan(...any) error }

func scanRuntimeThread(scanner runtimeThreadScanner) (RuntimeThreadRecord, error) {
	var record RuntimeThreadRecord
	var activeTurn sql.NullString
	if err := scanner.Scan(&record.Adapter, &record.ThreadID, &record.WorkspaceID, &record.Ownership, &record.State, &activeTurn); err != nil {
		if err == sql.ErrNoRows {
			return RuntimeThreadRecord{}, ErrNotFound
		}
		return RuntimeThreadRecord{}, internal("runtime thread read")
	}
	if activeTurn.Valid {
		record.ActiveTurnID = activeTurn.String
	}
	return record, nil
}

func validRuntimeThread(record RuntimeThreadRecord) bool {
	if record.Adapter != "codex" || !validWorkspaceText(record.ThreadID, 128) || !validWorkspaceText(record.WorkspaceID, 128) ||
		(record.Ownership != "created" && record.Ownership != "resumed") {
		return false
	}
	switch record.State {
	case RuntimeThreadIdle, RuntimeThreadStarting, RuntimeThreadNeedsReconcile:
		return record.ActiveTurnID == "" || record.State == RuntimeThreadNeedsReconcile && validWorkspaceText(record.ActiveTurnID, 128)
	case RuntimeThreadActive:
		return validWorkspaceText(record.ActiveTurnID, 128)
	default:
		return false
	}
}
