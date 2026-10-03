package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"

	_ "modernc.org/sqlite"
)

type Store struct {
	db                      *sql.DB
	capacityMu              sync.Mutex
	reservedVMIDs           map[int]struct{}
	capacityStateUpdateHook func(context.Context, string, string, string) error
}

func Open(path string) (*Store, error) {
	return OpenWithCapacityConfig(path, CapacityConfig{ReservedVMIDs: []int{3900}})
}

func OpenWithCapacityConfig(path string, config CapacityConfig) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout%3d5000&_pragma=foreign_keys%3d1")
	if err != nil {
		return nil, fmt.Errorf("open sqlite database: %w", err)
	}
	reserved, err := normalizeReservedVMIDs(config.ReservedVMIDs)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	store := &Store{db: db, reservedVMIDs: reserved}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(0)
	if err := store.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) RequireTable(ctx context.Context, name string) error {
	var found string
	err := s.db.QueryRowContext(ctx, "SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?", name).Scan(&found)
	if err == sql.ErrNoRows {
		return fmt.Errorf("required table %q is missing", name)
	}
	if err != nil {
		return fmt.Errorf("query table %q: %w", name, err)
	}
	return nil
}

func (s *Store) migrate(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY)"); err != nil {
		return fmt.Errorf("create migration ledger: %w", err)
	}
	for _, migration := range schemaMigrations {
		var applied int
		err := tx.QueryRowContext(ctx, "SELECT 1 FROM schema_migrations WHERE version = ?", migration.Version).Scan(&applied)
		if err == nil {
			continue
		}
		if err != sql.ErrNoRows {
			return fmt.Errorf("read migration %d: %w", migration.Version, err)
		}
		for _, statement := range migration.Statements {
			if migration.Version == 6 && (strings.HasPrefix(statement, "ALTER TABLE task_attempts") || strings.HasPrefix(statement, "CREATE TRIGGER IF NOT EXISTS task_attempts_route_snapshot_immutable")) {
				var exists int
				if err := tx.QueryRowContext(ctx, "SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = 'task_attempts'").Scan(&exists); errors.Is(err, sql.ErrNoRows) {
					continue
				} else if err != nil {
					return fmt.Errorf("check task_attempts table for migration %d: %w", migration.Version, err)
				}
			}
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("apply migration %d: %w", migration.Version, err)
			}
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO schema_migrations(version) VALUES (?)", migration.Version); err != nil {
			return fmt.Errorf("record migration %d: %w", migration.Version, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration: %w", err)
	}
	return nil
}

type schemaMigration struct {
	Version    int
	Statements []string
}

var schemaMigrations = []schemaMigration{{Version: 1, Statements: []string{
	"CREATE TABLE IF NOT EXISTS repositories (id TEXT PRIMARY KEY, remote_url TEXT NOT NULL, created_at TEXT NOT NULL)",
	"CREATE TABLE IF NOT EXISTS task_intake (idempotency_key TEXT PRIMARY KEY, task_id TEXT NOT NULL, request_hash TEXT NOT NULL, created_at TEXT NOT NULL)",
	"CREATE TABLE IF NOT EXISTS runs (id TEXT PRIMARY KEY, task_id TEXT NOT NULL, state TEXT NOT NULL, created_at TEXT NOT NULL)",
	"CREATE TABLE IF NOT EXISTS tasks (id TEXT PRIMARY KEY, repository_id TEXT NOT NULL, base_ref TEXT NOT NULL, objective TEXT NOT NULL, execution_class TEXT NOT NULL, state TEXT NOT NULL, created_at TEXT NOT NULL)",
	"CREATE TABLE IF NOT EXISTS task_attempts (id TEXT PRIMARY KEY, task_id TEXT NOT NULL, attempt_number INTEGER NOT NULL, model_profile TEXT NOT NULL, state TEXT NOT NULL, created_at TEXT NOT NULL, UNIQUE(task_id, attempt_number))",
	"CREATE TABLE IF NOT EXISTS execution_handles (id TEXT PRIMARY KEY, attempt_id TEXT NOT NULL, executor TEXT NOT NULL, external_id TEXT NOT NULL, state TEXT NOT NULL, UNIQUE(executor, external_id))",
	"CREATE TABLE IF NOT EXISTS capacity_nodes (id TEXT PRIMARY KEY, vmid INTEGER NOT NULL UNIQUE, generation TEXT NOT NULL, task_id TEXT, state TEXT NOT NULL, created_at TEXT NOT NULL)",
	"CREATE TABLE IF NOT EXISTS codex_threads (id TEXT PRIMARY KEY, attempt_id TEXT NOT NULL UNIQUE, codex_thread_id TEXT NOT NULL, created_at TEXT NOT NULL)",
	"CREATE TABLE IF NOT EXISTS task_messages (id TEXT PRIMARY KEY, task_id TEXT NOT NULL, attempt_id TEXT, role TEXT NOT NULL, body TEXT NOT NULL, created_at TEXT NOT NULL)",
	"CREATE TABLE IF NOT EXISTS task_events (id TEXT PRIMARY KEY, task_id TEXT NOT NULL, attempt_id TEXT, event_type TEXT NOT NULL, payload_json TEXT NOT NULL, created_at TEXT NOT NULL)",
	"CREATE TABLE IF NOT EXISTS model_profiles (id TEXT PRIMARY KEY, provider TEXT NOT NULL, model_id TEXT NOT NULL, enabled INTEGER NOT NULL)",
	"CREATE TABLE IF NOT EXISTS model_attempts (id TEXT PRIMARY KEY, attempt_id TEXT NOT NULL, model_profile_id TEXT NOT NULL, outcome TEXT NOT NULL, created_at TEXT NOT NULL)",
	"CREATE TABLE IF NOT EXISTS provider_health (provider TEXT PRIMARY KEY, state TEXT NOT NULL, consecutive_failures INTEGER NOT NULL, updated_at TEXT NOT NULL)",
	"CREATE TABLE IF NOT EXISTS git_refs (id TEXT PRIMARY KEY, task_id TEXT NOT NULL, attempt_id TEXT, branch TEXT NOT NULL, commit_sha TEXT, remote_state TEXT NOT NULL)",
	"CREATE TABLE IF NOT EXISTS validation_results (id TEXT PRIMARY KEY, attempt_id TEXT NOT NULL, command TEXT NOT NULL, state TEXT NOT NULL, output_ref TEXT, created_at TEXT NOT NULL)",
	"CREATE TABLE IF NOT EXISTS leases (id TEXT PRIMARY KEY, resource_type TEXT NOT NULL, resource_id TEXT NOT NULL, owner_id TEXT NOT NULL, expires_at TEXT NOT NULL, UNIQUE(resource_type, resource_id))",
	"CREATE TABLE IF NOT EXISTS commands (id TEXT PRIMARY KEY, task_id TEXT, attempt_id TEXT, command TEXT NOT NULL, state TEXT NOT NULL, output_ref TEXT, created_at TEXT NOT NULL)",
}}, {Version: 2, Statements: []string{
	"ALTER TABLE capacity_nodes ADD COLUMN attempt_id TEXT",
	"ALTER TABLE capacity_nodes ADD COLUMN priority INTEGER NOT NULL DEFAULT 1",
	"CREATE UNIQUE INDEX IF NOT EXISTS capacity_nodes_task_attempt ON capacity_nodes(task_id, attempt_id) WHERE task_id IS NOT NULL AND attempt_id IS NOT NULL",
}}, {Version: 3, Statements: []string{
	"CREATE TABLE IF NOT EXISTS release_progress (id TEXT PRIMARY KEY, task_id TEXT NOT NULL, attempt_id TEXT NOT NULL, vmid INTEGER NOT NULL, generation TEXT NOT NULL, kube_node TEXT NOT NULL, step TEXT NOT NULL, state TEXT NOT NULL, error_summary TEXT NOT NULL, updated_at TEXT NOT NULL, UNIQUE(task_id, attempt_id))",
}}, {Version: 4, Statements: []string{
	"CREATE TABLE IF NOT EXISTS attempt_execution_specs (id TEXT PRIMARY KEY, task_id TEXT NOT NULL, attempt_id TEXT NOT NULL UNIQUE, branch TEXT NOT NULL, validation_command_json TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)",
}}, {Version: 5, Statements: []string{
	"CREATE TABLE IF NOT EXISTS task_continuations (id TEXT PRIMARY KEY, task_id TEXT NOT NULL, attempt_id TEXT NOT NULL, idempotency_key TEXT NOT NULL, body TEXT NOT NULL, state TEXT NOT NULL, error_summary TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL, UNIQUE(task_id, attempt_id, idempotency_key))",
}}, {Version: 6, Statements: []string{
	"CREATE TABLE IF NOT EXISTS routing_state (id INTEGER PRIMARY KEY CHECK (id = 1), mode TEXT NOT NULL, observed_at TEXT NOT NULL, generation INTEGER NOT NULL)",
	"ALTER TABLE task_attempts ADD COLUMN route_mode TEXT",
	"ALTER TABLE task_attempts ADD COLUMN route_generation INTEGER",
	"ALTER TABLE task_attempts ADD COLUMN route_role TEXT",
	"ALTER TABLE task_attempts ADD COLUMN route_provider TEXT",
	"ALTER TABLE task_attempts ADD COLUMN route_model TEXT",
	"ALTER TABLE task_attempts ADD COLUMN route_wire_api TEXT",
	"ALTER TABLE task_attempts ADD COLUMN route_reasoning_effort TEXT",
	"ALTER TABLE task_attempts ADD COLUMN route_base_url TEXT",
	"ALTER TABLE task_attempts ADD COLUMN route_secret_name TEXT",
	"ALTER TABLE task_attempts ADD COLUMN route_secret_key TEXT",
	"CREATE TRIGGER IF NOT EXISTS task_attempts_route_snapshot_immutable BEFORE UPDATE OF route_mode, route_generation, route_role, route_provider, route_model, route_wire_api, route_reasoning_effort, route_base_url, route_secret_name, route_secret_key ON task_attempts WHEN OLD.route_mode IS NOT NEW.route_mode OR OLD.route_generation IS NOT NEW.route_generation OR OLD.route_role IS NOT NEW.route_role OR OLD.route_provider IS NOT NEW.route_provider OR OLD.route_model IS NOT NEW.route_model OR OLD.route_wire_api IS NOT NEW.route_wire_api OR OLD.route_reasoning_effort IS NOT NEW.route_reasoning_effort OR OLD.route_base_url IS NOT NEW.route_base_url OR OLD.route_secret_name IS NOT NEW.route_secret_name OR OLD.route_secret_key IS NOT NEW.route_secret_key BEGIN SELECT RAISE(ABORT, 'attempt route snapshot is immutable'); END",
}}, {Version: 7, Statements: []string{
	"CREATE TABLE IF NOT EXISTS sessions (id TEXT PRIMARY KEY, title TEXT NOT NULL, repository_id TEXT, workspace_id TEXT, state TEXT NOT NULL, preferred_backend TEXT NOT NULL, automatic_failover INTEGER NOT NULL DEFAULT 0 CHECK (automatic_failover IN (0, 1)), created_at TEXT NOT NULL, updated_at TEXT NOT NULL)",
	"CREATE TABLE IF NOT EXISTS session_epochs (id TEXT PRIMARY KEY, session_id TEXT NOT NULL REFERENCES sessions(id), sequence INTEGER NOT NULL, backend TEXT NOT NULL, identity_id TEXT NOT NULL, runtime_binding_id TEXT, provider_thread_id TEXT, state TEXT NOT NULL, started_at TEXT, ended_at TEXT, inbound_handoff_id TEXT, outbound_handoff_id TEXT, UNIQUE(session_id, sequence), UNIQUE(session_id, id), FOREIGN KEY(session_id, runtime_binding_id) REFERENCES session_runtime_bindings(session_id, id), FOREIGN KEY(session_id, inbound_handoff_id, id) REFERENCES session_handoffs(session_id, id, target_epoch_id), FOREIGN KEY(session_id, outbound_handoff_id, id) REFERENCES session_handoffs(session_id, id, source_epoch_id))",
	"CREATE UNIQUE INDEX IF NOT EXISTS session_epochs_one_active_per_session ON session_epochs(session_id) WHERE state = 'ACTIVE'",
	"CREATE TABLE IF NOT EXISTS session_runtime_bindings (id TEXT PRIMARY KEY, session_id TEXT NOT NULL REFERENCES sessions(id), epoch_id TEXT NOT NULL UNIQUE, runtime_id TEXT, vmid INTEGER, generation TEXT, state TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL, UNIQUE(session_id, id), FOREIGN KEY(session_id, epoch_id) REFERENCES session_epochs(session_id, id))",
	"CREATE TABLE IF NOT EXISTS session_handoffs (id TEXT PRIMARY KEY, session_id TEXT NOT NULL REFERENCES sessions(id), source_epoch_id TEXT NOT NULL, target_epoch_id TEXT, generation INTEGER NOT NULL, workspace_commit TEXT, workspace_dirty INTEGER CHECK (workspace_dirty IN (0, 1)), git_status_json TEXT, validation_json TEXT, machine_snapshot_json TEXT, semantic_handoff TEXT, state TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL, UNIQUE(session_id, generation), UNIQUE(session_id, id), UNIQUE(session_id, id, source_epoch_id), UNIQUE(session_id, id, target_epoch_id), FOREIGN KEY(session_id, source_epoch_id) REFERENCES session_epochs(session_id, id), FOREIGN KEY(session_id, target_epoch_id) REFERENCES session_epochs(session_id, id))",
	"CREATE TABLE IF NOT EXISTS session_events (id TEXT PRIMARY KEY, session_id TEXT NOT NULL REFERENCES sessions(id), epoch_id TEXT, event_type TEXT NOT NULL, payload_json TEXT NOT NULL, created_at TEXT NOT NULL, FOREIGN KEY(session_id, epoch_id) REFERENCES session_epochs(session_id, id))",
	"CREATE TABLE IF NOT EXISTS quota_observations (id TEXT PRIMARY KEY, identity_id TEXT NOT NULL, observed_at TEXT NOT NULL, source TEXT NOT NULL, freshness TEXT NOT NULL, five_hour_remaining_percent REAL CHECK (five_hour_remaining_percent IS NULL OR (five_hour_remaining_percent >= 0 AND five_hour_remaining_percent <= 100)), weekly_remaining_percent REAL CHECK (weekly_remaining_percent IS NULL OR (weekly_remaining_percent >= 0 AND weekly_remaining_percent <= 100)), five_hour_reset_at TEXT, weekly_reset_at TEXT, ordinary_usage_allowed INTEGER CHECK (ordinary_usage_allowed IS NULL OR ordinary_usage_allowed IN (0, 1)), status TEXT NOT NULL)",
	"CREATE TABLE IF NOT EXISTS session_routing_policies (session_id TEXT PRIMARY KEY REFERENCES sessions(id), preferred_backend TEXT NOT NULL, automatic_failover INTEGER NOT NULL DEFAULT 0 CHECK (automatic_failover IN (0, 1)), automatic_failback INTEGER NOT NULL DEFAULT 0 CHECK (automatic_failback IN (0, 1)), cross_account_allowed INTEGER NOT NULL DEFAULT 0 CHECK (cross_account_allowed IN (0, 1)), updated_at TEXT NOT NULL)",
	"CREATE TABLE IF NOT EXISTS session_transition_requests (id TEXT PRIMARY KEY, session_id TEXT NOT NULL REFERENCES sessions(id), generation INTEGER NOT NULL DEFAULT 1, idempotency_key TEXT NOT NULL, request_hash TEXT NOT NULL, source_epoch_id TEXT, target_backend TEXT NOT NULL, state TEXT NOT NULL, error_summary TEXT NOT NULL DEFAULT '', result_epoch_id TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL, UNIQUE(session_id, idempotency_key), UNIQUE(session_id, generation), FOREIGN KEY(session_id, source_epoch_id) REFERENCES session_epochs(session_id, id), FOREIGN KEY(session_id, result_epoch_id) REFERENCES session_epochs(session_id, id))",
	"CREATE TABLE IF NOT EXISTS delegated_session_tasks (id TEXT PRIMARY KEY, session_id TEXT NOT NULL REFERENCES sessions(id), epoch_id TEXT NOT NULL, idempotency_key TEXT NOT NULL, child_task_id TEXT NOT NULL UNIQUE, attempt_id TEXT, execution_handle_id TEXT, state TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL, UNIQUE(session_id, epoch_id, idempotency_key), FOREIGN KEY(session_id, epoch_id) REFERENCES session_epochs(session_id, id))",
}}, {Version: 8, Statements: []string{
	"ALTER TABLE sessions ADD COLUMN workspace_volume_id TEXT",
	"ALTER TABLE session_runtime_bindings ADD COLUMN pending_operation TEXT NOT NULL DEFAULT ''",
	"CREATE TEMP TABLE session_runtime_vmid_range_guard (vmid INTEGER CHECK (vmid BETWEEN 4000 AND 4999))",
	"INSERT INTO session_runtime_vmid_range_guard (vmid) SELECT vmid FROM session_runtime_bindings WHERE vmid IS NOT NULL AND vmid NOT BETWEEN 4000 AND 4999",
	"DROP TABLE session_runtime_vmid_range_guard",
	"CREATE TABLE IF NOT EXISTS session_runtime_binding_history (id INTEGER PRIMARY KEY AUTOINCREMENT, runtime_binding_id TEXT NOT NULL, session_id TEXT NOT NULL REFERENCES sessions(id), epoch_id TEXT NOT NULL, runtime_id TEXT, vmid INTEGER NOT NULL CHECK (vmid BETWEEN 4000 AND 4999), generation TEXT NOT NULL, final_state TEXT NOT NULL, created_at TEXT NOT NULL, deleted_at TEXT NOT NULL, FOREIGN KEY(session_id, runtime_binding_id, epoch_id) REFERENCES session_runtime_bindings(session_id, id, epoch_id), FOREIGN KEY(session_id, epoch_id) REFERENCES session_epochs(session_id, id))",
	"CREATE UNIQUE INDEX IF NOT EXISTS session_runtime_bindings_owner_epoch_id ON session_runtime_bindings(session_id, id, epoch_id)",
	"CREATE INDEX IF NOT EXISTS session_runtime_binding_history_epoch ON session_runtime_binding_history(session_id, epoch_id, id)",
	"CREATE TRIGGER IF NOT EXISTS session_runtime_binding_history_immutable_update BEFORE UPDATE ON session_runtime_binding_history BEGIN SELECT RAISE(ABORT, 'session runtime binding history is immutable'); END",
	"CREATE TRIGGER IF NOT EXISTS session_runtime_binding_history_immutable_delete BEFORE DELETE ON session_runtime_binding_history BEGIN SELECT RAISE(ABORT, 'session runtime binding history is immutable'); END",
	"CREATE UNIQUE INDEX IF NOT EXISTS session_runtime_bindings_vmid_reserved ON session_runtime_bindings(vmid) WHERE vmid IS NOT NULL AND state != 'DELETED'",
	"CREATE TRIGGER IF NOT EXISTS session_runtime_bindings_vmid_range_insert BEFORE INSERT ON session_runtime_bindings WHEN NEW.vmid IS NOT NULL AND NEW.vmid NOT BETWEEN 4000 AND 4999 BEGIN SELECT RAISE(ABORT, 'session runtime vmid is outside 4000-4999'); END",
	"CREATE TRIGGER IF NOT EXISTS session_runtime_bindings_vmid_range_update BEFORE UPDATE OF vmid ON session_runtime_bindings WHEN NEW.vmid IS NOT NULL AND NEW.vmid NOT BETWEEN 4000 AND 4999 BEGIN SELECT RAISE(ABORT, 'session runtime vmid is outside 4000-4999'); END",
}}, {Version: 9, Statements: []string{
	"CREATE TABLE IF NOT EXISTS session_bootstrap_checkpoints (runtime_binding_id TEXT NOT NULL REFERENCES session_runtime_bindings(id), generation TEXT NOT NULL, stage TEXT NOT NULL CHECK (stage IN ('isolation', 'guest_identity', 'host_pin', 'image_sanitized', 'network_enabled', 'artifact_verified', 'transport_verified')), status TEXT NOT NULL CHECK (status IN ('INTENT', 'UNKNOWN', 'COMPLETE')), evidence_sha256 TEXT NOT NULL DEFAULT '', CHECK (status != 'COMPLETE' OR (length(evidence_sha256) = 64 AND evidence_sha256 NOT GLOB '*[^0-9a-f]*')), PRIMARY KEY(runtime_binding_id, generation, stage))",
}}, {Version: 10, Statements: []string{
	"CREATE TABLE session_bootstrap_checkpoints_v10 (runtime_binding_id TEXT NOT NULL REFERENCES session_runtime_bindings(id), generation TEXT NOT NULL, stage TEXT NOT NULL CHECK (stage IN ('isolation', 'guest_identity', 'host_pin', 'image_sanitized', 'network_enabled', 'artifact_verified', 'transport_verified')), status TEXT NOT NULL CHECK (status IN ('INTENT', 'UNKNOWN', 'COMPLETE')), evidence_sha256 TEXT NOT NULL DEFAULT '', CHECK ((status IN ('INTENT', 'UNKNOWN') AND evidence_sha256 = '') OR (status = 'COMPLETE' AND length(evidence_sha256) = 64 AND evidence_sha256 NOT GLOB '*[^0-9a-f]*')), PRIMARY KEY(runtime_binding_id, generation, stage))",
	"INSERT INTO session_bootstrap_checkpoints_v10 (runtime_binding_id, generation, stage, status, evidence_sha256) SELECT runtime_binding_id, generation, stage, status, CASE WHEN status = 'COMPLETE' THEN evidence_sha256 ELSE '' END FROM session_bootstrap_checkpoints",
	"DROP TABLE session_bootstrap_checkpoints",
	"ALTER TABLE session_bootstrap_checkpoints_v10 RENAME TO session_bootstrap_checkpoints",
	"CREATE TRIGGER session_bootstrap_complete_immutable_update BEFORE UPDATE ON session_bootstrap_checkpoints WHEN OLD.status = 'COMPLETE' BEGIN SELECT RAISE(ABORT, 'completed Session bootstrap evidence is immutable'); END",
	"CREATE TRIGGER session_bootstrap_complete_immutable_delete BEFORE DELETE ON session_bootstrap_checkpoints WHEN OLD.status = 'COMPLETE' BEGIN SELECT RAISE(ABORT, 'completed Session bootstrap evidence is immutable'); END",
}}, {Version: 11, Statements: []string{
	"CREATE TRIGGER session_bootstrap_complete_insert_guard BEFORE INSERT ON session_bootstrap_checkpoints WHEN EXISTS (SELECT 1 FROM session_bootstrap_checkpoints WHERE runtime_binding_id = NEW.runtime_binding_id AND generation = NEW.generation AND stage = NEW.stage AND status = 'COMPLETE') BEGIN SELECT RAISE(ABORT, 'completed Session bootstrap evidence cannot be replaced'); END",
}}, {Version: 12, Statements: []string{
	"DROP TRIGGER session_bootstrap_complete_immutable_update",
	"DROP TRIGGER session_bootstrap_complete_immutable_delete",
	"DROP TRIGGER session_bootstrap_complete_insert_guard",
	"ALTER TABLE session_bootstrap_checkpoints RENAME TO session_bootstrap_checkpoints_v11",
	"CREATE TABLE session_bootstrap_checkpoints (runtime_binding_id TEXT NOT NULL REFERENCES session_runtime_bindings(id), generation TEXT NOT NULL, stage TEXT NOT NULL CHECK (stage IN ('isolation', 'guest_identity', 'host_pin', 'image_backup_created', 'image_sanitized', 'network_enabled', 'artifact_verified', 'transport_verified')), status TEXT NOT NULL CHECK (status IN ('INTENT', 'UNKNOWN', 'COMPLETE')), evidence_sha256 TEXT NOT NULL DEFAULT '', CHECK ((status IN ('INTENT', 'UNKNOWN') AND evidence_sha256 = '') OR (status = 'COMPLETE' AND length(evidence_sha256) = 64 AND evidence_sha256 NOT GLOB '*[^0-9a-f]*')), PRIMARY KEY(runtime_binding_id, generation, stage))",
	"INSERT INTO session_bootstrap_checkpoints (runtime_binding_id, generation, stage, status, evidence_sha256) SELECT runtime_binding_id, generation, stage, status, evidence_sha256 FROM session_bootstrap_checkpoints_v11",
	"DROP TABLE session_bootstrap_checkpoints_v11",
	"CREATE TRIGGER session_bootstrap_complete_immutable_update BEFORE UPDATE ON session_bootstrap_checkpoints WHEN OLD.status = 'COMPLETE' BEGIN SELECT RAISE(ABORT, 'completed Session bootstrap evidence is immutable'); END",
	"CREATE TRIGGER session_bootstrap_complete_immutable_delete BEFORE DELETE ON session_bootstrap_checkpoints WHEN OLD.status = 'COMPLETE' BEGIN SELECT RAISE(ABORT, 'completed Session bootstrap evidence is immutable'); END",
	"CREATE TRIGGER session_bootstrap_complete_insert_guard BEFORE INSERT ON session_bootstrap_checkpoints WHEN EXISTS (SELECT 1 FROM session_bootstrap_checkpoints WHERE runtime_binding_id = NEW.runtime_binding_id AND generation = NEW.generation AND stage = NEW.stage AND status = 'COMPLETE') BEGIN SELECT RAISE(ABORT, 'completed Session bootstrap evidence cannot be replaced'); END",
}}, {Version: 13, Statements: []string{
	"DROP TRIGGER session_bootstrap_complete_immutable_update",
	"DROP TRIGGER session_bootstrap_complete_immutable_delete",
	"DROP TRIGGER session_bootstrap_complete_insert_guard",
	"ALTER TABLE session_bootstrap_checkpoints RENAME TO session_bootstrap_checkpoints_v12",
	"CREATE TABLE session_bootstrap_checkpoints (runtime_binding_id TEXT NOT NULL REFERENCES session_runtime_bindings(id), generation TEXT NOT NULL, stage TEXT NOT NULL CHECK (stage IN ('isolation', 'guest_identity', 'host_pin', 'image_backup_created', 'image_sanitized', 'network_enabled', 'helper_verified', 'artifact_verified', 'transport_verified')), status TEXT NOT NULL CHECK (status IN ('INTENT', 'UNKNOWN', 'COMPLETE')), evidence_sha256 TEXT NOT NULL DEFAULT '', CHECK ((status IN ('INTENT', 'UNKNOWN') AND evidence_sha256 = '') OR (status = 'COMPLETE' AND length(evidence_sha256) = 64 AND evidence_sha256 NOT GLOB '*[^0-9a-f]*')), PRIMARY KEY(runtime_binding_id, generation, stage))",
	"INSERT INTO session_bootstrap_checkpoints (runtime_binding_id, generation, stage, status, evidence_sha256) SELECT runtime_binding_id, generation, stage, status, evidence_sha256 FROM session_bootstrap_checkpoints_v12",
	"DROP TABLE session_bootstrap_checkpoints_v12",
	"CREATE TRIGGER session_bootstrap_complete_immutable_update BEFORE UPDATE ON session_bootstrap_checkpoints WHEN OLD.status = 'COMPLETE' BEGIN SELECT RAISE(ABORT, 'completed Session bootstrap evidence is immutable'); END",
	"CREATE TRIGGER session_bootstrap_complete_immutable_delete BEFORE DELETE ON session_bootstrap_checkpoints WHEN OLD.status = 'COMPLETE' BEGIN SELECT RAISE(ABORT, 'completed Session bootstrap evidence is immutable'); END",
	"CREATE TRIGGER session_bootstrap_complete_insert_guard BEFORE INSERT ON session_bootstrap_checkpoints WHEN EXISTS (SELECT 1 FROM session_bootstrap_checkpoints WHERE runtime_binding_id = NEW.runtime_binding_id AND generation = NEW.generation AND stage = NEW.stage AND status = 'COMPLETE') BEGIN SELECT RAISE(ABORT, 'completed Session bootstrap evidence cannot be replaced'); END",
}}}
