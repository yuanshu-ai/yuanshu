package store

import (
	"context"
	"database/sql"
	"time"
)

const CurrentSchemaVersion = 2

type migration struct {
	version    int
	name       string
	statements []string
}

var serverMigrations = []migration{{
	version: 1,
	name:    "server_bootstrap_and_metadata",
	statements: []string{
		`CREATE TABLE owners (
			id TEXT PRIMARY KEY CHECK (length(id) BETWEEN 1 AND 128),
			singleton INTEGER NOT NULL UNIQUE CHECK (singleton = 1),
			status TEXT NOT NULL CHECK (status IN ('active', 'disabled')),
			created_at TEXT NOT NULL
		) STRICT`,
		`CREATE TABLE nodes (
			id TEXT PRIMARY KEY CHECK (length(id) BETWEEN 1 AND 128),
			owner_id TEXT NOT NULL,
			public_key BLOB NOT NULL UNIQUE CHECK (length(public_key) = 32),
			name TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 128),
			os TEXT NOT NULL CHECK (os IN ('windows', 'linux', 'darwin')),
			version TEXT NOT NULL CHECK (length(version) BETWEEN 1 AND 64),
			status TEXT NOT NULL CHECK (status IN ('active', 'revoked')),
			created_at TEXT NOT NULL,
			last_seen_at TEXT,
			FOREIGN KEY (owner_id) REFERENCES owners(id) ON DELETE CASCADE
		) STRICT`,
		`CREATE INDEX nodes_owner ON nodes(owner_id, id)`,
		`CREATE TABLE node_credentials (
			node_id TEXT PRIMARY KEY,
			credential_hash BLOB NOT NULL UNIQUE CHECK (length(credential_hash) = 32),
			status TEXT NOT NULL CHECK (status IN ('active', 'revoked')),
			created_at TEXT NOT NULL,
			rotated_at TEXT,
			revoked_at TEXT,
			FOREIGN KEY (node_id) REFERENCES nodes(id) ON DELETE CASCADE
		) STRICT`,
		`CREATE TABLE control_clients (
			id TEXT PRIMARY KEY CHECK (length(id) BETWEEN 1 AND 128),
			owner_id TEXT NOT NULL,
			public_key BLOB NOT NULL UNIQUE CHECK (length(public_key) = 32),
			name TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 128),
			status TEXT NOT NULL CHECK (status IN ('active', 'revoked')),
			created_at TEXT NOT NULL,
			revoked_at TEXT,
			FOREIGN KEY (owner_id) REFERENCES owners(id) ON DELETE CASCADE
		) STRICT`,
		`CREATE INDEX control_clients_owner ON control_clients(owner_id, id)`,
		`CREATE TABLE bootstrap (
			singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
			status TEXT NOT NULL CHECK (status IN ('pending', 'completed')),
			secret_hash BLOB CHECK (secret_hash IS NULL OR length(secret_hash) = 32),
			claim_digest BLOB CHECK (claim_digest IS NULL OR length(claim_digest) = 32),
			owner_id TEXT,
			node_id TEXT,
			issued_at TEXT NOT NULL,
			claimed_at TEXT,
			retry_until TEXT,
			CHECK (
				(status = 'pending' AND secret_hash IS NOT NULL AND claim_digest IS NULL AND owner_id IS NULL AND node_id IS NULL AND claimed_at IS NULL AND retry_until IS NULL)
				OR
				(status = 'completed' AND owner_id IS NOT NULL AND node_id IS NOT NULL AND claimed_at IS NOT NULL)
			),
			FOREIGN KEY (owner_id) REFERENCES owners(id),
			FOREIGN KEY (node_id) REFERENCES nodes(id)
		) STRICT`,
	},
}, {
	version: 2,
	name:    "control_client_pairing",
	statements: []string{
		`ALTER TABLE control_clients ADD COLUMN key_id TEXT NOT NULL DEFAULT 'primary' CHECK (length(key_id) BETWEEN 1 AND 128)`,
		`CREATE TABLE pairings (
			id TEXT PRIMARY KEY CHECK (length(id) BETWEEN 1 AND 128),
			owner_id TEXT NOT NULL,
			node_id TEXT NOT NULL,
			code_hash BLOB NOT NULL UNIQUE CHECK (length(code_hash) = 32),
			challenge BLOB NOT NULL CHECK (length(challenge) = 32),
			status TEXT NOT NULL CHECK (status IN ('pending', 'claimed', 'approved', 'declined', 'expired')),
			expires_at TEXT NOT NULL,
			client_id TEXT,
			key_id TEXT,
			public_key BLOB CHECK (public_key IS NULL OR length(public_key) = 32),
			client_name TEXT,
			created_at TEXT NOT NULL,
			claimed_at TEXT,
			resolved_at TEXT,
			proof BLOB CHECK (proof IS NULL OR length(proof) = 64),
			FOREIGN KEY (owner_id) REFERENCES owners(id) ON DELETE CASCADE,
			FOREIGN KEY (node_id) REFERENCES nodes(id) ON DELETE CASCADE,
			CHECK (
				(status = 'pending' AND client_id IS NULL AND key_id IS NULL AND public_key IS NULL AND client_name IS NULL AND claimed_at IS NULL AND resolved_at IS NULL AND proof IS NULL)
				OR (status = 'claimed' AND client_id IS NOT NULL AND key_id IS NOT NULL AND public_key IS NOT NULL AND client_name IS NOT NULL AND claimed_at IS NOT NULL AND resolved_at IS NULL AND proof IS NULL)
				OR (status IN ('approved', 'declined') AND client_id IS NOT NULL AND key_id IS NOT NULL AND public_key IS NOT NULL AND client_name IS NOT NULL AND claimed_at IS NOT NULL AND resolved_at IS NOT NULL AND proof IS NOT NULL)
				OR (status = 'expired' AND resolved_at IS NOT NULL)
			)
		) STRICT`,
		`CREATE INDEX pairings_node_status ON pairings(node_id, status, expires_at)`,
	},
}}

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
	for _, item := range serverMigrations {
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
		if _, err := tx.ExecContext(ctx, "PRAGMA user_version = "+schemaLiteral(item.version)); err != nil {
			return internal("migration")
		}
	}
	if err := tx.Commit(); err != nil {
		return internal("migration")
	}
	return nil
}

func schemaLiteral(version int) string {
	switch version {
	case 1:
		return "1"
	case 2:
		return "2"
	default:
		panic("unsupported server schema version")
	}
}
