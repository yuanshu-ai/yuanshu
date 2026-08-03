package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

const (
	ConfigChangePending  = "pending"
	ConfigChangeApproved = "approved"
	ConfigChangeRejected = "rejected"
)

type ConfigChangeRecord struct {
	ID           string
	BaseRevision string
	Changes      json.RawMessage
	State        string
	CreatedAt    time.Time
	UpdatedAt    time.Time
	ErrorCode    string
}

func (s *Store) CreateConfigChange(ctx context.Context, record ConfigChangeRecord) (ConfigChangeRecord, error) {
	if err := requireContext(ctx); err != nil {
		return ConfigChangeRecord{}, err
	}
	if !validWorkspaceText(record.ID, 128) || !validWorkspaceText(record.BaseRevision, 128) || len(record.Changes) < 2 || len(record.Changes) > 262144 || record.State != ConfigChangePending {
		return ConfigChangeRecord{}, ErrInvalid
	}
	db, err := s.database()
	if err != nil {
		return ConfigChangeRecord{}, err
	}
	now := record.CreatedAt
	if now.IsZero() {
		now = s.clock()
	}
	_, err = db.ExecContext(ctx, `INSERT INTO config_changes(id,base_revision,changes,state,created_at,updated_at,error_code) VALUES(?,?,?,?,?,?,NULL)`, record.ID, record.BaseRevision, []byte(record.Changes), record.State, timestamp(now), timestamp(now))
	if err != nil {
		return ConfigChangeRecord{}, internal("config change create")
	}
	return s.ConfigChange(ctx, record.ID)
}

func (s *Store) ConfigChange(ctx context.Context, id string) (ConfigChangeRecord, error) {
	if err := requireContext(ctx); err != nil {
		return ConfigChangeRecord{}, err
	}
	if !validWorkspaceText(id, 128) {
		return ConfigChangeRecord{}, ErrInvalid
	}
	db, err := s.database()
	if err != nil {
		return ConfigChangeRecord{}, err
	}
	return scanConfigChange(db.QueryRowContext(ctx, `SELECT id,base_revision,changes,state,created_at,updated_at,error_code FROM config_changes WHERE id=?`, id))
}

func (s *Store) ConfigChanges(ctx context.Context, state string) ([]ConfigChangeRecord, error) {
	if err := requireContext(ctx); err != nil {
		return nil, err
	}
	if state != "" && state != ConfigChangePending && state != ConfigChangeApproved && state != ConfigChangeRejected {
		return nil, ErrInvalid
	}
	db, err := s.database()
	if err != nil {
		return nil, err
	}
	query := `SELECT id,base_revision,changes,state,created_at,updated_at,error_code FROM config_changes ORDER BY created_at,id`
	args := []any{}
	if state != "" {
		query = `SELECT id,base_revision,changes,state,created_at,updated_at,error_code FROM config_changes WHERE state=? ORDER BY created_at,id`
		args = append(args, state)
	}
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, internal("config changes list")
	}
	defer rows.Close()
	result := make([]ConfigChangeRecord, 0)
	for rows.Next() {
		item, scanErr := scanConfigChange(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, internal("config changes list")
	}
	return result, nil
}

func (s *Store) TransitionConfigChange(ctx context.Context, id, nextState, errorCode string) (ConfigChangeRecord, error) {
	if err := requireContext(ctx); err != nil {
		return ConfigChangeRecord{}, err
	}
	if !validWorkspaceText(id, 128) || (nextState != ConfigChangeApproved && nextState != ConfigChangeRejected) || len(errorCode) > 128 {
		return ConfigChangeRecord{}, ErrInvalid
	}
	db, err := s.database()
	if err != nil {
		return ConfigChangeRecord{}, err
	}
	result, err := db.ExecContext(ctx, `UPDATE config_changes SET state=?,error_code=?,updated_at=? WHERE id=? AND state='pending'`, nextState, nullText(errorCode), timestamp(s.clock()), id)
	if err != nil {
		return ConfigChangeRecord{}, internal("config change transition")
	}
	count, err := result.RowsAffected()
	if err != nil || count != 1 {
		return ConfigChangeRecord{}, ErrConflict
	}
	return s.ConfigChange(ctx, id)
}

func scanConfigChange(scanner interface{ Scan(...any) error }) (ConfigChangeRecord, error) {
	var result ConfigChangeRecord
	var changes []byte
	var created, updated string
	var errorCode sql.NullString
	if err := scanner.Scan(&result.ID, &result.BaseRevision, &changes, &result.State, &created, &updated, &errorCode); err != nil {
		if err == sql.ErrNoRows {
			return ConfigChangeRecord{}, ErrNotFound
		}
		return ConfigChangeRecord{}, internal("config change read")
	}
	var err error
	result.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return ConfigChangeRecord{}, ErrCorrupt
	}
	result.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
	if err != nil {
		return ConfigChangeRecord{}, ErrCorrupt
	}
	result.Changes = append(json.RawMessage(nil), changes...)
	result.ErrorCode = errorCode.String
	return result, nil
}
