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
	{
		version: 3,
		name:    "codex_runtime_thread_ownership",
		statements: []string{
			`CREATE TABLE runtime_threads (
				adapter TEXT NOT NULL CHECK (adapter = 'codex'),
				thread_id TEXT PRIMARY KEY CHECK (length(thread_id) BETWEEN 1 AND 128),
				workspace_id TEXT NOT NULL CHECK (length(workspace_id) BETWEEN 1 AND 128),
				ownership TEXT NOT NULL CHECK (ownership IN ('created', 'resumed')),
				state TEXT NOT NULL CHECK (state IN ('idle', 'starting', 'active', 'needs_reconcile')),
				active_turn_id TEXT CHECK (active_turn_id IS NULL OR length(active_turn_id) BETWEEN 1 AND 128),
				updated_at TEXT NOT NULL,
				CHECK ((state = 'active' AND active_turn_id IS NOT NULL) OR state != 'active')
			) STRICT`,
			`CREATE INDEX runtime_threads_workspace ON runtime_threads(workspace_id, thread_id)`,
		},
	},
	{
		version: 4,
		name:    "event_log_recovery",
		statements: []string{
			`CREATE TABLE event_streams (
				owner_id TEXT NOT NULL CHECK (length(owner_id) BETWEEN 1 AND 128),
				node_id TEXT NOT NULL CHECK (length(node_id) BETWEEN 1 AND 128),
				stream_id TEXT NOT NULL CHECK (length(stream_id) BETWEEN 1 AND 128),
				latest_sequence INTEGER NOT NULL DEFAULT 0 CHECK (latest_sequence BETWEEN 0 AND 9007199254740991),
				total_bytes INTEGER NOT NULL DEFAULT 0 CHECK (total_bytes >= 0),
				updated_at TEXT NOT NULL,
				PRIMARY KEY (owner_id, node_id, stream_id)
			) STRICT`,
			`CREATE TABLE event_log (
				owner_id TEXT NOT NULL,
				node_id TEXT NOT NULL,
				stream_id TEXT NOT NULL,
				sequence INTEGER NOT NULL CHECK (sequence BETWEEN 1 AND 9007199254740991),
				message_id TEXT NOT NULL UNIQUE CHECK (length(message_id) BETWEEN 1 AND 128),
				event_type TEXT NOT NULL CHECK (length(event_type) BETWEEN 1 AND 128),
				workspace_id TEXT,
				thread_id TEXT,
				turn_id TEXT,
				item_id TEXT,
				frame BLOB NOT NULL CHECK (length(frame) <= 1048576),
				frame_bytes INTEGER NOT NULL CHECK (frame_bytes = length(frame)),
				created_at TEXT NOT NULL,
				PRIMARY KEY (owner_id, node_id, stream_id, sequence),
				FOREIGN KEY (owner_id, node_id, stream_id) REFERENCES event_streams(owner_id, node_id, stream_id) ON DELETE CASCADE
			) STRICT`,
			`CREATE INDEX event_log_retention ON event_log(owner_id, node_id, stream_id, created_at, sequence)`,
			`CREATE INDEX event_log_thread ON event_log(thread_id, sequence)`,
			`CREATE TABLE event_cursors (
				owner_id TEXT NOT NULL,
				node_id TEXT NOT NULL,
				stream_id TEXT NOT NULL,
				acknowledged_sequence INTEGER NOT NULL CHECK (acknowledged_sequence BETWEEN 0 AND 9007199254740991),
				updated_at TEXT NOT NULL,
				PRIMARY KEY (owner_id, node_id, stream_id),
				FOREIGN KEY (owner_id, node_id, stream_id) REFERENCES event_streams(owner_id, node_id, stream_id) ON DELETE CASCADE
			) STRICT`,
			`CREATE TABLE thread_snapshots (
				thread_id TEXT PRIMARY KEY CHECK (length(thread_id) BETWEEN 1 AND 128),
				workspace_id TEXT NOT NULL CHECK (length(workspace_id) BETWEEN 1 AND 128),
				status TEXT NOT NULL CHECK (length(status) BETWEEN 1 AND 64),
				latest_sequence INTEGER NOT NULL CHECK (latest_sequence BETWEEN 0 AND 9007199254740991),
				payload BLOB NOT NULL CHECK (length(payload) <= 786432),
				updated_at TEXT NOT NULL
			) STRICT`,
			`CREATE TABLE approval_state (
				approval_id TEXT PRIMARY KEY CHECK (length(approval_id) BETWEEN 1 AND 128),
				workspace_id TEXT NOT NULL CHECK (length(workspace_id) BETWEEN 1 AND 128),
				thread_id TEXT NOT NULL CHECK (length(thread_id) BETWEEN 1 AND 128),
				turn_id TEXT NOT NULL CHECK (length(turn_id) BETWEEN 1 AND 128),
				item_id TEXT NOT NULL CHECK (length(item_id) BETWEEN 1 AND 128),
				status TEXT NOT NULL CHECK (status IN ('pending', 'resolved', 'ambiguous')),
				operation_digest TEXT CHECK (operation_digest IS NULL OR length(operation_digest) = 43),
				payload BLOB NOT NULL CHECK (length(payload) <= 786432),
				expires_at TEXT,
				updated_at TEXT NOT NULL
			) STRICT`,
			`CREATE INDEX approval_state_thread ON approval_state(thread_id, turn_id, status)`,
			`CREATE TABLE control_requests (
				message_id TEXT PRIMARY KEY CHECK (length(message_id) BETWEEN 1 AND 128),
				request_digest BLOB NOT NULL CHECK (length(request_digest) = 32),
				control_type TEXT NOT NULL CHECK (length(control_type) BETWEEN 1 AND 128),
				workspace_id TEXT,
				thread_id TEXT,
				turn_id TEXT,
				item_id TEXT,
				state TEXT NOT NULL CHECK (state IN ('received', 'validated', 'dispatching', 'confirmed', 'rejected', 'ambiguous')),
				error_code TEXT,
				result_stream_id TEXT,
				result_sequence INTEGER,
				created_at TEXT NOT NULL,
				updated_at TEXT NOT NULL
			) STRICT`,
		},
	},
	{
		version: 5,
		name:    "owner_control_trust_manifest",
		statements: []string{
			`CREATE TABLE trust_manifests (
				owner_id TEXT NOT NULL CHECK (length(owner_id) BETWEEN 1 AND 128),
				node_id TEXT NOT NULL CHECK (length(node_id) BETWEEN 1 AND 128),
				revision INTEGER NOT NULL CHECK (revision >= 0),
				updated_at TEXT NOT NULL,
				PRIMARY KEY (owner_id, node_id)
			) STRICT`,
		},
	},
	{
		version: 6,
		name:    "approval_resolution_state_machine",
		statements: []string{
			`DROP INDEX approval_state_thread`,
			`ALTER TABLE approval_state RENAME TO approval_state_legacy`,
			`CREATE TABLE approval_state (
				approval_id TEXT PRIMARY KEY CHECK (length(approval_id) BETWEEN 1 AND 128),
				workspace_id TEXT NOT NULL CHECK (length(workspace_id) BETWEEN 1 AND 128),
				thread_id TEXT NOT NULL CHECK (length(thread_id) BETWEEN 1 AND 128),
				turn_id TEXT NOT NULL CHECK (length(turn_id) BETWEEN 1 AND 128),
				item_id TEXT NOT NULL CHECK (length(item_id) BETWEEN 1 AND 128),
				status TEXT NOT NULL CHECK (status IN ('pending', 'processing', 'accepted', 'declined', 'resolved', 'expired', 'ambiguous')),
				operation_digest TEXT CHECK (operation_digest IS NULL OR length(operation_digest) = 43),
				payload BLOB NOT NULL CHECK (length(payload) <= 786432),
				expires_at TEXT,
				updated_at TEXT NOT NULL
			) STRICT`,
			`INSERT INTO approval_state(approval_id,workspace_id,thread_id,turn_id,item_id,status,operation_digest,payload,expires_at,updated_at)
				SELECT approval_id,workspace_id,thread_id,turn_id,item_id,status,operation_digest,payload,expires_at,updated_at FROM approval_state_legacy`,
			`DROP TABLE approval_state_legacy`,
			`CREATE INDEX approval_state_thread ON approval_state(thread_id, turn_id, status)`,
		},
	},
	{
		version: 7,
		name:    "pending_node_config_changes",
		statements: []string{
			`CREATE TABLE config_changes (
				id TEXT PRIMARY KEY CHECK (length(id) BETWEEN 1 AND 128),
				base_revision TEXT NOT NULL CHECK (length(base_revision) BETWEEN 1 AND 128),
				changes BLOB NOT NULL CHECK (length(changes) BETWEEN 2 AND 262144),
				state TEXT NOT NULL CHECK (state IN ('pending', 'approved', 'rejected')),
				created_at TEXT NOT NULL,
				updated_at TEXT NOT NULL,
				error_code TEXT
			) STRICT`,
			`CREATE INDEX config_changes_state ON config_changes(state, created_at)`,
		},
	},
	{
		version: 8,
		name:    "agent_resources_and_task_bindings",
		statements: []string{
			`CREATE TABLE agent_installations (
				adapter_type TEXT PRIMARY KEY CHECK (length(adapter_type) BETWEEN 1 AND 64),
				installation_state TEXT NOT NULL CHECK (installation_state IN ('installed', 'not_installed', 'incompatible', 'unavailable')),
				version TEXT NOT NULL CHECK (length(version) <= 128),
				compatibility TEXT NOT NULL CHECK (compatibility IN ('known', 'unverified', 'unsupported')),
				process_state TEXT NOT NULL CHECK (process_state IN ('running', 'stopped', 'unknown')),
				process_count INTEGER NOT NULL CHECK (process_count BETWEEN 0 AND 1024),
				detected_at TEXT NOT NULL
			) STRICT`,
			`CREATE TABLE agent_instances (
				instance_id TEXT PRIMARY KEY CHECK (length(instance_id) BETWEEN 1 AND 128),
				adapter_type TEXT NOT NULL CHECK (length(adapter_type) BETWEEN 1 AND 64),
				display_name TEXT NOT NULL CHECK (length(display_name) BETWEEN 1 AND 128),
				enabled INTEGER NOT NULL CHECK (enabled IN (0, 1)),
				is_default INTEGER NOT NULL CHECK (is_default IN (0, 1)),
				runtime_mode TEXT NOT NULL CHECK (runtime_mode IN ('managed', 'detected-only')),
				config_revision TEXT NOT NULL CHECK (length(config_revision) BETWEEN 1 AND 128),
				updated_at TEXT NOT NULL
			) STRICT`,
			`CREATE UNIQUE INDEX agent_instances_single_default ON agent_instances(is_default) WHERE is_default = 1`,
			`CREATE TABLE runtime_endpoints (
				endpoint_id TEXT PRIMARY KEY CHECK (length(endpoint_id) BETWEEN 1 AND 128),
				instance_id TEXT NOT NULL REFERENCES agent_instances(instance_id) ON DELETE CASCADE,
				mode TEXT NOT NULL CHECK (mode IN ('managed', 'detected-only')),
				ownership TEXT NOT NULL CHECK (ownership IN ('node', 'external')),
				updated_at TEXT NOT NULL,
				UNIQUE(instance_id, endpoint_id)
			) STRICT`,
			`CREATE TABLE workspace_agents (
				workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
				instance_id TEXT NOT NULL REFERENCES agent_instances(instance_id) ON DELETE CASCADE,
				is_default INTEGER NOT NULL CHECK (is_default IN (0, 1)),
				updated_at TEXT NOT NULL,
				PRIMARY KEY(workspace_id, instance_id)
			) STRICT`,
			`CREATE UNIQUE INDEX workspace_agents_single_default ON workspace_agents(workspace_id) WHERE is_default = 1`,
			`CREATE TABLE task_bindings (
				task_id TEXT PRIMARY KEY CHECK (length(task_id) BETWEEN 1 AND 128),
				instance_id TEXT NOT NULL REFERENCES agent_instances(instance_id) ON DELETE CASCADE,
				endpoint_id TEXT NOT NULL REFERENCES runtime_endpoints(endpoint_id) ON DELETE CASCADE,
				workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
				native_session_id TEXT NOT NULL CHECK (length(native_session_id) BETWEEN 1 AND 256),
				ownership TEXT NOT NULL CHECK (ownership IN ('created', 'resumed')),
				state TEXT NOT NULL CHECK (state IN ('idle', 'starting', 'active', 'needs_reconcile')),
				active_run_id TEXT CHECK (active_run_id IS NULL OR length(active_run_id) BETWEEN 1 AND 128),
				legacy_native_id_exposed INTEGER NOT NULL DEFAULT 0 CHECK (legacy_native_id_exposed IN (0, 1)),
				updated_at TEXT NOT NULL,
				UNIQUE(instance_id, native_session_id),
				CHECK ((state = 'active' AND active_run_id IS NOT NULL) OR state != 'active')
			) STRICT`,
			`CREATE INDEX task_bindings_workspace ON task_bindings(workspace_id, instance_id, task_id)`,
			`INSERT INTO agent_instances(instance_id,adapter_type,display_name,enabled,is_default,runtime_mode,config_revision,updated_at)
				VALUES ('codex-default','codex','Codex',1,1,'managed','legacy-v1',CURRENT_TIMESTAMP)`,
			`INSERT INTO runtime_endpoints(endpoint_id,instance_id,mode,ownership,updated_at)
				VALUES ('codex-default-managed','codex-default','managed','node',CURRENT_TIMESTAMP)`,
			`INSERT INTO workspace_agents(workspace_id,instance_id,is_default,updated_at)
				SELECT id,'codex-default',1,CURRENT_TIMESTAMP FROM workspaces`,
			`INSERT INTO task_bindings(task_id,instance_id,endpoint_id,workspace_id,native_session_id,ownership,state,active_run_id,legacy_native_id_exposed,updated_at)
				SELECT r.thread_id,'codex-default','codex-default-managed',r.workspace_id,r.thread_id,r.ownership,r.state,r.active_turn_id,1,r.updated_at
				FROM runtime_threads r JOIN workspaces w ON w.id = r.workspace_id`,
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
	case 3:
		return "3"
	case 4:
		return "4"
	case 5:
		return "5"
	case 6:
		return "6"
	case 7:
		return "7"
	case 8:
		return "8"
	default:
		panic(errors.New("unsupported schema version"))
	}
}
