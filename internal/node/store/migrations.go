package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type migration struct {
	version    int
	name       string
	statements []string
}

var nodeMigrations = []migration{
	{
		version: 1,
		name:    "node_identity_security_and_outbox",
		statements: []string{
			`CREATE TABLE identity (
			singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
			algorithm TEXT NOT NULL CHECK (algorithm = 'ed25519'),
			public_key BLOB NOT NULL CHECK (length(public_key) = 32),
			private_key_ref TEXT NOT NULL CHECK (length(private_key_ref) BETWEEN 1 AND 512),
			owner_id TEXT,
			node_id TEXT,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			CHECK ((owner_id IS NULL AND node_id IS NULL) OR (owner_id IS NOT NULL AND node_id IS NOT NULL))
		) STRICT`,
			`CREATE TABLE trusted_clients (
			owner_id TEXT NOT NULL,
			node_id TEXT NOT NULL,
			client_id TEXT NOT NULL,
			key_id TEXT NOT NULL,
			public_key BLOB NOT NULL CHECK (length(public_key) = 32),
			status TEXT NOT NULL CHECK (status IN ('active', 'revoked')),
			created_at TEXT NOT NULL,
			revoked_at TEXT,
			PRIMARY KEY (owner_id, node_id, client_id, key_id)
		) STRICT`,
			`CREATE TABLE replay_messages (
			owner_id TEXT NOT NULL,
			node_id TEXT NOT NULL,
			message_id TEXT NOT NULL,
			accepted_at TEXT NOT NULL,
			PRIMARY KEY (owner_id, node_id, message_id)
		) STRICT`,
			`CREATE TABLE replay_nonces (
			owner_id TEXT NOT NULL,
			node_id TEXT NOT NULL,
			client_id TEXT NOT NULL,
			key_id TEXT NOT NULL,
			nonce TEXT NOT NULL,
			retain_until INTEGER NOT NULL,
			PRIMARY KEY (owner_id, node_id, client_id, key_id, nonce)
		) STRICT`,
			`CREATE TABLE signer_sequences (
			owner_id TEXT NOT NULL,
			node_id TEXT NOT NULL,
			client_id TEXT NOT NULL,
			key_id TEXT NOT NULL,
			highest_sequence INTEGER NOT NULL CHECK (highest_sequence >= 0 AND highest_sequence <= 9007199254740991),
			updated_at TEXT NOT NULL,
			PRIMARY KEY (owner_id, node_id, client_id, key_id)
		) STRICT`,
			`CREATE TABLE outbox (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			message_id TEXT NOT NULL UNIQUE CHECK (length(message_id) BETWEEN 1 AND 128),
			stream_id TEXT NOT NULL CHECK (length(stream_id) BETWEEN 1 AND 128),
			sequence INTEGER NOT NULL CHECK (sequence >= 0 AND sequence <= 9007199254740991),
			frame BLOB NOT NULL CHECK (length(frame) <= 1048576),
			created_at TEXT NOT NULL,
			acknowledged_at TEXT,
			UNIQUE (stream_id, sequence)
		) STRICT`,
			`CREATE INDEX outbox_pending_order ON outbox(acknowledged_at, id)`,
			`CREATE INDEX replay_nonce_expiry ON replay_nonces(retain_until)`,
		},
	},
	{
		version: 2,
		name:    "workspace_policy",
		statements: []string{
			`CREATE TABLE workspaces (
				id TEXT PRIMARY KEY CHECK (length(id) BETWEEN 1 AND 128),
				display_name TEXT NOT NULL CHECK (length(display_name) BETWEEN 1 AND 128),
				canonical_path TEXT NOT NULL CHECK (length(canonical_path) BETWEEN 1 AND 32767),
				filesystem_root TEXT NOT NULL CHECK (length(filesystem_root) BETWEEN 1 AND 32767),
				file_identity TEXT NOT NULL UNIQUE CHECK (length(file_identity) BETWEEN 1 AND 256),
				adapter TEXT NOT NULL CHECK (adapter = 'codex'),
				permission_profile TEXT NOT NULL CHECK (permission_profile IN ('read-only', 'workspace-write')),
				allow_network INTEGER NOT NULL CHECK (allow_network IN (0, 1)),
				updated_at TEXT NOT NULL
			) STRICT`,
		},
	},
}

func runMigrations(ctx context.Context, db *sql.DB, now time.Time) error {
	var userVersion int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&userVersion); err != nil {
		return ErrCorrupt
	}
	if userVersion > CurrentSchemaVersion {
		return ErrFutureSchema
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return internal("migration")
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		name TEXT NOT NULL,
		applied_at TEXT NOT NULL
	) STRICT`); err != nil {
		return ErrCorrupt
	}
	var current int
	if err := tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&current); err != nil {
		return ErrCorrupt
	}
	if current > CurrentSchemaVersion {
		return ErrFutureSchema
	}
	if userVersion != 0 && userVersion != current {
		return ErrCorrupt
	}
	for _, item := range nodeMigrations {
		if item.version <= current {
			continue
		}
		for _, statement := range item.statements {
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return internal("migration")
			}
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO schema_migrations(version, name, applied_at) VALUES (?, ?, ?)", item.version, item.name, timestamp(now)); err != nil {
			return internal("migration")
		}
		if _, err := tx.ExecContext(ctx, "PRAGMA user_version = "+sqlLiteralInt(item.version)); err != nil {
			return internal("migration")
		}
	}
	if err := tx.Commit(); err != nil {
		return internal("migration")
	}
	return nil
}

func sqlLiteralInt(value int) string {
	switch value {
	case 1:
		return "1"
	case 2:
		return "2"
	default:
		panic(errors.New("unsupported schema version"))
	}
}
