package store

import (
	"context"
	"database/sql"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	workspaceReadOnly       = "read-only"
	workspaceWorkspaceWrite = "workspace-write"
)

type WorkspaceRecord struct {
	ID                string
	DisplayName       string
	CanonicalPath     string
	FilesystemRoot    string
	FileIdentity      string
	Adapter           string
	PermissionProfile string
	AllowNetwork      bool
}

func (s *Store) ReplaceWorkspaces(ctx context.Context, records []WorkspaceRecord) error {
	if err := requireContext(ctx); err != nil {
		return err
	}
	if !validWorkspaceRecords(records) {
		return ErrInvalid
	}
	db, err := s.database()
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return internal("workspace transaction")
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "DELETE FROM workspaces"); err != nil {
		return internal("workspace replace")
	}
	statement := `INSERT INTO workspaces(
		id, display_name, canonical_path, filesystem_root, file_identity,
		adapter, permission_profile, allow_network, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`
	updatedAt := timestamp(s.clock().UTC())
	for _, record := range records {
		if _, err := tx.ExecContext(ctx, statement,
			record.ID,
			record.DisplayName,
			record.CanonicalPath,
			record.FilesystemRoot,
			record.FileIdentity,
			record.Adapter,
			record.PermissionProfile,
			boolInteger(record.AllowNetwork),
			updatedAt,
		); err != nil {
			return internal("workspace replace")
		}
	}
	if err := tx.Commit(); err != nil {
		return internal("workspace replace")
	}
	return nil
}

func (s *Store) Workspace(ctx context.Context, id string) (WorkspaceRecord, error) {
	if err := requireContext(ctx); err != nil {
		return WorkspaceRecord{}, err
	}
	if !validWorkspaceText(id, 128) {
		return WorkspaceRecord{}, ErrInvalid
	}
	db, err := s.database()
	if err != nil {
		return WorkspaceRecord{}, err
	}
	row := db.QueryRowContext(ctx, `SELECT
		id, display_name, canonical_path, filesystem_root, file_identity,
		adapter, permission_profile, allow_network
		FROM workspaces WHERE id = ?`, id)
	record, err := scanWorkspace(row)
	if err != nil {
		if err == sql.ErrNoRows {
			return WorkspaceRecord{}, ErrNotFound
		}
		return WorkspaceRecord{}, internal("workspace read")
	}
	return record, nil
}

func (s *Store) Workspaces(ctx context.Context) ([]WorkspaceRecord, error) {
	if err := requireContext(ctx); err != nil {
		return nil, err
	}
	db, err := s.database()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT
		id, display_name, canonical_path, filesystem_root, file_identity,
		adapter, permission_profile, allow_network
		FROM workspaces ORDER BY id`)
	if err != nil {
		return nil, internal("workspace list")
	}
	defer rows.Close()
	records := make([]WorkspaceRecord, 0)
	for rows.Next() {
		record, err := scanWorkspace(rows)
		if err != nil {
			return nil, internal("workspace list")
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, internal("workspace list")
	}
	return records, nil
}

type workspaceScanner interface {
	Scan(dest ...any) error
}

func scanWorkspace(scanner workspaceScanner) (WorkspaceRecord, error) {
	var record WorkspaceRecord
	var allowNetwork int
	err := scanner.Scan(
		&record.ID,
		&record.DisplayName,
		&record.CanonicalPath,
		&record.FilesystemRoot,
		&record.FileIdentity,
		&record.Adapter,
		&record.PermissionProfile,
		&allowNetwork,
	)
	if err != nil {
		return WorkspaceRecord{}, err
	}
	record.AllowNetwork = allowNetwork == 1
	return record, nil
}

func validWorkspaceRecords(records []WorkspaceRecord) bool {
	if records == nil {
		return false
	}
	ids := make(map[string]struct{}, len(records))
	identities := make(map[string]struct{}, len(records))
	for _, record := range records {
		if !validWorkspaceText(record.ID, 128) ||
			!validWorkspaceText(record.DisplayName, 128) ||
			!validWorkspaceText(record.CanonicalPath, 32767) ||
			!validWorkspaceText(record.FilesystemRoot, 32767) ||
			!validWorkspaceText(record.FileIdentity, 256) ||
			record.Adapter != "codex" ||
			(record.PermissionProfile != workspaceReadOnly && record.PermissionProfile != workspaceWorkspaceWrite) {
			return false
		}
		if _, exists := ids[record.ID]; exists {
			return false
		}
		ids[record.ID] = struct{}{}
		if _, exists := identities[record.FileIdentity]; exists {
			return false
		}
		identities[record.FileIdentity] = struct{}{}
	}
	return true
}

func validWorkspaceText(value string, maxBytes int) bool {
	if !utf8.ValidString(value) || strings.TrimSpace(value) == "" || len(value) > maxBytes {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func boolInteger(value bool) int {
	if value {
		return 1
	}
	return 0
}
