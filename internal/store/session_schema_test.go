package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
)

func TestSessionSchemaMigrationCreatesDistinctEntitiesAndConstraints(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "sessions.sqlite"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	for _, table := range []string{
		"sessions",
		"session_epochs",
		"session_runtime_bindings",
		"session_handoffs",
		"session_events",
		"quota_observations",
		"session_routing_policies",
		"session_transition_requests",
		"delegated_session_tasks",
	} {
		if err := store.RequireTable(context.Background(), table); err != nil {
			t.Errorf("RequireTable(%q) error = %v", table, err)
		}
	}

	var migrationVersion int
	if err := store.db.QueryRow("SELECT MAX(version) FROM schema_migrations").Scan(&migrationVersion); err != nil {
		t.Fatalf("read migration head: %v", err)
	}
	if got, want := schemaMigrations[len(schemaMigrations)-1].Version, migrationVersion; got != want {
		t.Errorf("migration head = %d, recorded head = %d", got, want)
	}
	if len(schemaMigrations) < 2 || schemaMigrations[len(schemaMigrations)-1].Version != schemaMigrations[len(schemaMigrations)-2].Version+1 {
		t.Errorf("session migration version %d does not immediately follow prior migration", schemaMigrations[len(schemaMigrations)-1].Version)
	}

	if _, err := store.db.Exec(`
		INSERT INTO sessions (id, title, state, preferred_backend, automatic_failover, created_at, updated_at)
		VALUES ('session-1', 'test', 'ACTIVE', 'codex-a', 0, 'now', 'now'), ('session-2', 'other', 'ACTIVE', 'codex-a', 0, 'now', 'now');
		INSERT INTO session_epochs (id, session_id, sequence, backend, identity_id, state, started_at)
		VALUES ('epoch-1', 'session-1', 1, 'codex-a', 'identity-a', 'ACTIVE', 'now'), ('epoch-other', 'session-2', 1, 'codex-a', 'identity-a', 'STOPPED', 'now')`); err != nil {
		t.Fatalf("insert first active epoch: %v", err)
	}
	if _, err := store.db.Exec(`INSERT INTO session_epochs (id, session_id, sequence, backend, identity_id, state, started_at)
		VALUES ('epoch-2', 'session-1', 2, 'spark-glm', 'identity-spark', 'ACTIVE', 'now')`); err == nil {
		t.Fatal("insert second ACTIVE epoch for one session succeeded, want partial unique index violation")
	}
	if _, err := store.db.Exec(`INSERT INTO session_epochs (id, session_id, sequence, backend, identity_id, state, started_at)
		VALUES ('epoch-3', 'session-1', 2, 'spark-glm', 'identity-spark', 'STOPPED', 'now')`); err != nil {
		t.Fatalf("insert non-active epoch alongside active epoch: %v", err)
	}

	if _, err := store.db.Exec(`INSERT INTO quota_observations (id, identity_id, observed_at, source, freshness, status)
		VALUES ('quota-unknown', 'identity-a', 'now', 'probe', 'fresh', 'UNKNOWN')`); err != nil {
		t.Fatalf("insert quota observation with unknown values: %v", err)
	}
	var fiveHour, weekly sql.NullFloat64
	if err := store.db.QueryRow(`SELECT five_hour_remaining_percent, weekly_remaining_percent FROM quota_observations WHERE id = 'quota-unknown'`).Scan(&fiveHour, &weekly); err != nil {
		t.Fatalf("read unknown quota values: %v", err)
	}
	if fiveHour.Valid || weekly.Valid {
		t.Fatalf("missing quota values = (%v, %v), want SQL NULL", fiveHour, weekly)
	}
	if _, err := store.db.Exec(`INSERT INTO quota_observations (id, identity_id, observed_at, source, freshness, five_hour_remaining_percent, status)
		VALUES ('quota-invalid', 'identity-a', 'now', 'probe', 'fresh', 101, 'HEALTHY')`); err == nil {
		t.Fatal("quota percentage above 100 succeeded, want CHECK constraint failure")
	}

	if _, err := store.db.Exec(`INSERT INTO session_transition_requests (id, session_id, idempotency_key, request_hash, target_backend, state, created_at, updated_at)
		VALUES ('transition-1', 'session-1', 'switch-1', 'hash-a', 'spark-glm', 'REQUESTED', 'now', 'now')`); err != nil {
		t.Fatalf("insert transition request: %v", err)
	}
	if _, err := store.db.Exec(`INSERT INTO session_transition_requests (id, session_id, idempotency_key, request_hash, target_backend, state, created_at, updated_at)
		VALUES ('transition-2', 'session-1', 'switch-1', 'hash-a', 'spark-glm', 'REQUESTED', 'now', 'now')`); err == nil {
		t.Fatal("duplicate transition idempotency key succeeded")
	}
	if _, err := store.db.Exec(`INSERT INTO session_transition_requests (id, session_id, generation, idempotency_key, request_hash, target_backend, state, created_at, updated_at)
		VALUES ('transition-3', 'session-1', 1, 'switch-2', 'hash-b', 'codex-a', 'REQUESTED', 'now', 'now')`); err == nil {
		t.Fatal("second transition request in one generation succeeded, want unique-generation constraint failure")
	}
	if _, err := store.db.Exec(`INSERT INTO session_transition_requests (id, session_id, generation, idempotency_key, request_hash, target_backend, state, created_at, updated_at)
		VALUES ('transition-4', 'session-1', 2, 'switch-3', 'hash-c', 'codex-a', 'REQUESTED', 'now', 'now')`); err != nil {
		t.Fatalf("insert transition for a new generation: %v", err)
	}

	if _, err := store.db.Exec(`INSERT INTO delegated_session_tasks (id, session_id, epoch_id, idempotency_key, child_task_id, state, created_at, updated_at)
		VALUES ('delegation-1', 'session-1', 'epoch-1', 'child-1', 'task-1', 'DISPATCHED', 'now', 'now')`); err != nil {
		t.Fatalf("insert delegated task mapping: %v", err)
	}
	if _, err := store.db.Exec(`INSERT INTO delegated_session_tasks (id, session_id, epoch_id, idempotency_key, child_task_id, state, created_at, updated_at)
		VALUES ('delegation-2', 'session-1', 'epoch-1', 'child-1', 'task-2', 'DISPATCHED', 'now', 'now')`); err == nil {
		t.Fatal("duplicate delegated-task idempotency mapping succeeded")
	}
	if _, err := store.db.Exec(`INSERT INTO delegated_session_tasks (id, session_id, epoch_id, idempotency_key, child_task_id, state, created_at, updated_at)
		VALUES ('delegation-3', 'session-1', 'epoch-1', 'child-2', 'task-1', 'DISPATCHED', 'now', 'now')`); err == nil {
		t.Fatal("mapping one child task more than once succeeded")
	}
	if _, err := store.db.Exec(`INSERT INTO delegated_session_tasks (id, session_id, epoch_id, idempotency_key, child_task_id, state, created_at, updated_at)
		VALUES ('delegation-cross-session', 'session-1', 'epoch-other', 'child-cross', 'task-cross', 'DISPATCHED', 'now', 'now')`); err == nil {
		t.Fatal("delegated task linked a session to another session's epoch")
	}
	if _, err := store.db.Exec(`INSERT INTO session_runtime_bindings (id, session_id, epoch_id, state, created_at, updated_at)
		VALUES ('binding-cross-session', 'session-1', 'epoch-other', 'BOUND', 'now', 'now')`); err == nil {
		t.Fatal("runtime binding linked a session to another session's epoch")
	}
	if _, err := store.db.Exec(`INSERT INTO session_handoffs (id, session_id, source_epoch_id, generation, state, created_at, updated_at)
		VALUES ('handoff-cross-session', 'session-1', 'epoch-other', 10, 'READY', 'now', 'now')`); err == nil {
		t.Fatal("handoff linked a session to another session's source epoch")
	}
	if _, err := store.db.Exec(`INSERT INTO session_events (id, session_id, epoch_id, event_type, payload_json, created_at)
		VALUES ('event-cross-session', 'session-1', 'epoch-other', 'TEST', '{}', 'now')`); err == nil {
		t.Fatal("event linked a session to another session's epoch")
	}
	if _, err := store.db.Exec(`INSERT INTO session_transition_requests (id, session_id, generation, idempotency_key, request_hash, source_epoch_id, target_backend, state, created_at, updated_at)
		VALUES ('transition-cross-session', 'session-1', 10, 'switch-cross', 'hash-cross', 'epoch-other', 'spark-glm', 'REQUESTED', 'now', 'now')`); err == nil {
		t.Fatal("transition linked a session to another session's source epoch")
	}
	if _, err := store.db.Exec(`INSERT INTO session_handoffs (id, session_id, source_epoch_id, target_epoch_id, generation, state, created_at, updated_at)
		VALUES ('handoff-cross-target', 'session-1', 'epoch-1', 'epoch-other', 11, 'READY', 'now', 'now')`); err == nil {
		t.Fatal("handoff linked a session to another session's target epoch")
	}
	if _, err := store.db.Exec(`INSERT INTO session_transition_requests (id, session_id, generation, idempotency_key, request_hash, target_backend, state, result_epoch_id, created_at, updated_at)
		VALUES ('transition-cross-result', 'session-1', 11, 'switch-result-cross', 'hash-result-cross', 'codex-a', 'COMPLETE', 'epoch-other', 'now', 'now')`); err == nil {
		t.Fatal("transition linked a session to another session's result epoch")
	}
	if _, err := store.db.Exec(`INSERT INTO session_runtime_bindings (id, session_id, epoch_id, state, created_at, updated_at)
		VALUES ('binding-valid', 'session-1', 'epoch-1', 'BOUND', 'now', 'now')`); err != nil {
		t.Fatalf("insert binding for matching session epoch: %v", err)
	}
	if _, err := store.db.Exec("UPDATE session_epochs SET runtime_binding_id = 'binding-valid' WHERE id = 'epoch-1'"); err != nil {
		t.Fatalf("link epoch to its runtime binding: %v", err)
	}
	if _, err := store.db.Exec(`INSERT INTO session_handoffs (id, session_id, source_epoch_id, generation, state, created_at, updated_at)
		VALUES ('handoff-other', 'session-2', 'epoch-other', 1, 'READY', 'now', 'now')`); err != nil {
		t.Fatalf("insert handoff for matching session epoch: %v", err)
	}
	if _, err := store.db.Exec("UPDATE session_epochs SET inbound_handoff_id = 'handoff-other' WHERE id = 'epoch-1'"); err == nil {
		t.Fatal("epoch linked an inbound handoff belonging to another session")
	}
	if _, err := store.db.Exec(`INSERT INTO session_handoffs (id, session_id, source_epoch_id, target_epoch_id, generation, state, created_at, updated_at)
		VALUES ('handoff-valid', 'session-1', 'epoch-1', 'epoch-3', 12, 'READY', 'now', 'now')`); err != nil {
		t.Fatalf("insert handoff between epochs in matching session: %v", err)
	}
	if _, err := store.db.Exec("UPDATE session_epochs SET outbound_handoff_id = 'handoff-valid' WHERE id = 'epoch-1'"); err != nil {
		t.Fatalf("link epoch to its outbound handoff: %v", err)
	}
	if _, err := store.db.Exec("UPDATE session_epochs SET inbound_handoff_id = 'handoff-valid' WHERE id = 'epoch-3'"); err != nil {
		t.Fatalf("link target epoch to its inbound handoff: %v", err)
	}
	if _, err := store.db.Exec("UPDATE session_epochs SET outbound_handoff_id = 'handoff-valid' WHERE id = 'epoch-3'"); err == nil {
		t.Fatal("epoch linked an outbound handoff whose source is a different epoch")
	}
	if _, err := store.db.Exec("UPDATE session_epochs SET inbound_handoff_id = 'handoff-valid' WHERE id = 'epoch-1'"); err == nil {
		t.Fatal("epoch linked an inbound handoff whose target is a different epoch")
	}

	assertForeignKeyDeclaration(t, store.db, "session_epochs", "session_id", "sessions")
	assertForeignKeyDeclaration(t, store.db, "session_epochs", "runtime_binding_id", "session_runtime_bindings")
	assertForeignKeyDeclaration(t, store.db, "session_runtime_bindings", "epoch_id", "session_epochs")
	assertColumnExists(t, store.db, "session_epochs", "inbound_handoff_id")
	assertColumnExists(t, store.db, "session_epochs", "outbound_handoff_id")
	var foreignKeysEnabled int
	if err := store.db.QueryRow("PRAGMA foreign_keys").Scan(&foreignKeysEnabled); err != nil {
		t.Fatalf("read foreign_keys pragma: %v", err)
	}
	if foreignKeysEnabled != 0 {
		t.Logf("foreign key enforcement is enabled on this connection (PRAGMA foreign_keys=%d)", foreignKeysEnabled)
	} else {
		t.Log("foreign key declarations exist but this connection does not enforce them (PRAGMA foreign_keys=0)")
	}
	if foreignKeysEnabled != 1 {
		t.Fatal("foreign key enforcement is disabled, want PRAGMA foreign_keys=1")
	}
	if _, err := store.db.Exec(`INSERT INTO session_epochs (id, session_id, sequence, backend, identity_id, state)
		VALUES ('orphan-epoch', 'missing-session', 1, 'codex-a', 'identity-a', 'STOPPED')`); err == nil {
		t.Fatal("session epoch with missing parent succeeded, want foreign key constraint failure")
	}

	if _, err := store.db.Exec(`INSERT INTO task_attempts (id, task_id, attempt_number, model_profile, state, created_at)
		VALUES ('legacy-attempt', 'legacy-task', 1, 'legacy-profile', 'UNKNOWN', 'now')`); err != nil {
		t.Fatalf("existing task_attempts schema changed incompatibly: %v", err)
	}
}

func assertColumnExists(t *testing.T, db *sql.DB, table, column string) {
	t.Helper()
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		t.Fatalf("query columns for %s: %v", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, dataType string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatalf("scan column for %s: %v", table, err)
		}
		if name == column {
			return
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate columns for %s: %v", table, err)
	}
	t.Errorf("%s has no %s column", table, column)
}

func assertForeignKeyDeclaration(t *testing.T, db *sql.DB, table, from, target string) {
	t.Helper()
	rows, err := db.Query("PRAGMA foreign_key_list(" + table + ")")
	if err != nil {
		t.Fatalf("query foreign keys for %s: %v", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, seq int
		var referencedTable, referencedColumn, fromColumn, onUpdate, onDelete, match string
		if err := rows.Scan(&id, &seq, &referencedTable, &fromColumn, &referencedColumn, &onUpdate, &onDelete, &match); err != nil {
			t.Fatalf("scan foreign key for %s: %v", table, err)
		}
		if fromColumn == from && referencedTable == target {
			return
		}
	}
	if err := rows.Err(); err != nil && !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("iterate foreign keys for %s: %v", table, err)
	}
	t.Errorf("%s.%s has no declared foreign key to %s", table, from, target)
}
