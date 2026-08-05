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
	AgentInstanceID string
	Adapter         string
	ThreadID        string
	WorkspaceID     string
	Ownership       string
	State           string
	ActiveTurnID    string
}

func (s *Store) SaveRuntimeThread(ctx context.Context, record RuntimeThreadRecord) error {
	if err := requireContext(ctx); err != nil {
		return err
	}
	if record.AgentInstanceID == "" {
		record.AgentInstanceID = "codex-default"
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
	if !runtimeThreadsUseInstances(ctx, db) {
		_, err = db.ExecContext(ctx, `INSERT INTO runtime_threads(
			adapter, thread_id, workspace_id, ownership, state, active_turn_id, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(thread_id) DO UPDATE SET adapter=excluded.adapter,workspace_id=excluded.workspace_id,
			ownership=excluded.ownership,state=excluded.state,active_turn_id=excluded.active_turn_id,updated_at=excluded.updated_at`,
			record.Adapter, record.ThreadID, record.WorkspaceID, record.Ownership, record.State, activeTurn, timestamp(s.clock().UTC()))
		if err != nil {
			return internal("runtime thread save")
		}
		return nil
	}
	_, err = db.ExecContext(ctx, `INSERT INTO runtime_threads(
		agent_instance_id, adapter, thread_id, workspace_id, ownership, state, active_turn_id, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(agent_instance_id, thread_id) DO UPDATE SET
		adapter=excluded.adapter,
		workspace_id=excluded.workspace_id,
		ownership=excluded.ownership,
		state=excluded.state,
		active_turn_id=excluded.active_turn_id,
		updated_at=excluded.updated_at`,
		record.AgentInstanceID, record.Adapter, record.ThreadID, record.WorkspaceID, record.Ownership,
		record.State, activeTurn, timestamp(s.clock().UTC()))
	if err != nil {
		return internal("runtime thread save")
	}
	return nil
}

func (s *Store) RuntimeThread(ctx context.Context, threadID string) (RuntimeThreadRecord, error) {
	return s.RuntimeThreadForInstance(ctx, "codex-default", threadID)
}

func (s *Store) RuntimeThreadForInstance(ctx context.Context, instanceID, threadID string) (RuntimeThreadRecord, error) {
	if err := requireContext(ctx); err != nil {
		return RuntimeThreadRecord{}, err
	}
	if !validWorkspaceText(instanceID, 128) || !validWorkspaceText(threadID, 256) {
		return RuntimeThreadRecord{}, ErrInvalid
	}
	db, err := s.database()
	if err != nil {
		return RuntimeThreadRecord{}, err
	}
	if !runtimeThreadsUseInstances(ctx, db) {
		return scanLegacyRuntimeThread(db.QueryRowContext(ctx, `SELECT adapter, thread_id, workspace_id, ownership, state, active_turn_id
			FROM runtime_threads WHERE thread_id = ?`, threadID), instanceID)
	}
	return scanRuntimeThread(db.QueryRowContext(ctx, `SELECT agent_instance_id, adapter, thread_id, workspace_id, ownership, state, active_turn_id
		FROM runtime_threads WHERE agent_instance_id = ? AND thread_id = ?`, instanceID, threadID))
}

func (s *Store) RuntimeThreads(ctx context.Context) ([]RuntimeThreadRecord, error) {
	if err := requireContext(ctx); err != nil {
		return nil, err
	}
	db, err := s.database()
	if err != nil {
		return nil, err
	}
	if !runtimeThreadsUseInstances(ctx, db) {
		return s.legacyRuntimeThreads(ctx, db, "codex-default")
	}
	rows, err := db.QueryContext(ctx, `SELECT agent_instance_id, adapter, thread_id, workspace_id, ownership, state, active_turn_id
		FROM runtime_threads ORDER BY agent_instance_id, thread_id`)
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

func (s *Store) RuntimeThreadsForInstance(ctx context.Context, instanceID string) ([]RuntimeThreadRecord, error) {
	if err := requireContext(ctx); err != nil {
		return nil, err
	}
	if !validWorkspaceText(instanceID, 128) {
		return nil, ErrInvalid
	}
	db, err := s.database()
	if err != nil {
		return nil, err
	}
	if !runtimeThreadsUseInstances(ctx, db) {
		if instanceID != "codex-default" {
			return nil, nil
		}
		return s.legacyRuntimeThreads(ctx, db, instanceID)
	}
	rows, err := db.QueryContext(ctx, `SELECT agent_instance_id, adapter, thread_id, workspace_id, ownership, state, active_turn_id
		FROM runtime_threads WHERE agent_instance_id = ? ORDER BY thread_id`, instanceID)
	if err != nil {
		return nil, internal("runtime thread list")
	}
	defer rows.Close()
	records := make([]RuntimeThreadRecord, 0)
	for rows.Next() {
		record, scanErr := scanRuntimeThread(rows)
		if scanErr != nil {
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
	return s.DeleteRuntimeThreadForInstance(ctx, "codex-default", threadID)
}

func (s *Store) DeleteRuntimeThreadForInstance(ctx context.Context, instanceID, threadID string) error {
	if err := requireContext(ctx); err != nil {
		return err
	}
	if !validWorkspaceText(instanceID, 128) || !validWorkspaceText(threadID, 256) {
		return ErrInvalid
	}
	db, err := s.database()
	if err != nil {
		return err
	}
	statement := "DELETE FROM runtime_threads WHERE agent_instance_id = ? AND thread_id = ?"
	arguments := []any{instanceID, threadID}
	if !runtimeThreadsUseInstances(ctx, db) {
		if instanceID != "codex-default" {
			return ErrNotFound
		}
		statement, arguments = "DELETE FROM runtime_threads WHERE thread_id = ?", []any{threadID}
	}
	result, err := db.ExecContext(ctx, statement, arguments...)
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
	if err := scanner.Scan(&record.AgentInstanceID, &record.Adapter, &record.ThreadID, &record.WorkspaceID, &record.Ownership, &record.State, &activeTurn); err != nil {
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

func scanLegacyRuntimeThread(scanner runtimeThreadScanner, instanceID string) (RuntimeThreadRecord, error) {
	var record RuntimeThreadRecord
	var activeTurn sql.NullString
	if err := scanner.Scan(&record.Adapter, &record.ThreadID, &record.WorkspaceID, &record.Ownership, &record.State, &activeTurn); err != nil {
		if err == sql.ErrNoRows {
			return RuntimeThreadRecord{}, ErrNotFound
		}
		return RuntimeThreadRecord{}, internal("runtime thread read")
	}
	record.AgentInstanceID = instanceID
	if activeTurn.Valid {
		record.ActiveTurnID = activeTurn.String
	}
	return record, nil
}

func (s *Store) legacyRuntimeThreads(ctx context.Context, db *sql.DB, instanceID string) ([]RuntimeThreadRecord, error) {
	rows, err := db.QueryContext(ctx, `SELECT adapter, thread_id, workspace_id, ownership, state, active_turn_id FROM runtime_threads ORDER BY thread_id`)
	if err != nil {
		return nil, internal("runtime thread list")
	}
	defer rows.Close()
	var records []RuntimeThreadRecord
	for rows.Next() {
		record, scanErr := scanLegacyRuntimeThread(rows, instanceID)
		if scanErr != nil {
			return nil, internal("runtime thread list")
		}
		records = append(records, record)
	}
	if rows.Err() != nil {
		return nil, internal("runtime thread list")
	}
	return records, nil
}

func runtimeThreadsUseInstances(ctx context.Context, db *sql.DB) bool {
	var count int
	return db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('runtime_threads') WHERE name='agent_instance_id'`).Scan(&count) == nil && count == 1
}

func validRuntimeThread(record RuntimeThreadRecord) bool {
	if !validWorkspaceText(record.AgentInstanceID, 128) || record.Adapter != "codex" || !validWorkspaceText(record.ThreadID, 256) || !validWorkspaceText(record.WorkspaceID, 128) ||
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
