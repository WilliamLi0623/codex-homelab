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
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout%3d5000")
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
}}}
