package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestSessionRuntimeBindingReservesVMIDAndSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "sessions.sqlite")
	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	now := time.Now().UTC()
	if err := store.CreateSession(ctx, Session{ID: "session-1", Title: "one", State: "READY", PreferredBackend: "codex-a", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	if err := store.CreateSessionEpoch(ctx, SessionEpoch{ID: "epoch-1", SessionID: "session-1", Sequence: 1, Backend: "codex-a", IdentityID: "identity-a", State: "STARTING", StartedAt: now}); err != nil {
		t.Fatalf("CreateSessionEpoch() error = %v", err)
	}
	binding := SessionRuntimeBinding{ID: "binding-1", SessionID: "session-1", EpochID: "epoch-1", VMID: 4000, Generation: "gen-1", State: "ALLOCATING", CreatedAt: now, UpdatedAt: now}
	if err := store.CreateSessionRuntimeBinding(ctx, binding); err != nil {
		t.Fatalf("CreateSessionRuntimeBinding() error = %v", err)
	}
	if err := store.UpdateSessionRuntimeBindingState(ctx, binding.ID, "ALLOCATING", "CREATING", "lxc-4000", now.Add(time.Second)); err != nil {
		t.Fatalf("UpdateSessionRuntimeBindingState() error = %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	store, err = Open(path)
	if err != nil {
		t.Fatalf("reopen Store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	got, err := store.GetSessionRuntimeBinding(ctx, binding.SessionID, binding.EpochID)
	if err != nil {
		t.Fatalf("GetSessionRuntimeBinding() error = %v", err)
	}
	if got.VMID != binding.VMID || got.Generation != binding.Generation || got.State != "CREATING" || got.RuntimeID != "lxc-4000" || got.PendingOperation != "create" {
		t.Fatalf("persisted binding = %+v, want VMID/generation/state/runtime preserved", got)
	}
	if err := store.UpdateSessionRuntimeBindingState(ctx, binding.ID, "CREATING", "UNKNOWN", "", now.Add(2*time.Second)); err != nil {
		t.Fatalf("mark create outcome unknown: %v", err)
	}
	got, err = store.GetSessionRuntimeBinding(ctx, binding.SessionID, binding.EpochID)
	if err != nil || got.State != "UNKNOWN" || got.PendingOperation != "create" {
		t.Fatalf("unknown binding intent = %+v, error = %v; want create intent preserved", got, err)
	}
	if err := store.UpdateSessionRuntimeBindingState(ctx, binding.ID, "STARTING", "READY", "lxc-4000", now.Add(2*time.Second)); !errors.Is(err, ErrSessionRuntimeBindingConflict) {
		t.Fatalf("stale expected state update error = %v, want state conflict", err)
	}
}

func TestAllocateSessionRuntimeBindingAtomicallyReservesFirstFreeVMID(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "sessions.sqlite"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Now().UTC()
	for index, id := range []string{"session-1", "session-2"} {
		if err := store.CreateSession(ctx, Session{ID: id, Title: id, State: "READY", PreferredBackend: "codex-a", CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatalf("CreateSession(%s) error = %v", id, err)
		}
		epochID := "epoch-" + id
		if err := store.CreateSessionEpoch(ctx, SessionEpoch{ID: epochID, SessionID: id, Sequence: 1, Backend: "codex-a", IdentityID: "identity-a", State: "STARTING", StartedAt: now}); err != nil {
			t.Fatalf("CreateSessionEpoch(%s) error = %v", epochID, err)
		}
		binding, err := store.AllocateSessionRuntimeBinding(ctx, SessionRuntimeBinding{ID: "binding-" + id, SessionID: id, EpochID: epochID, Generation: "gen-" + id, State: "ALLOCATING", CreatedAt: now, UpdatedAt: now})
		if err != nil {
			t.Fatalf("AllocateSessionRuntimeBinding(%s) error = %v", id, err)
		}
		wantVMID := SessionVMIDMin + index
		if binding.VMID != wantVMID {
			t.Fatalf("allocated VMID = %d, want %d", binding.VMID, wantVMID)
		}
		persisted, err := store.GetSessionRuntimeBinding(ctx, id, epochID)
		if err != nil || persisted.ID != binding.ID || persisted.VMID != wantVMID {
			t.Fatalf("persisted allocation = %+v, error = %v", persisted, err)
		}
	}
}

func TestConcurrentSessionRuntimeAllocationsNeverShareVMID(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "sessions.sqlite"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Now().UTC()
	const count = 16
	for index := 0; index < count; index++ {
		sessionID := fmt.Sprintf("session-%02d", index)
		epochID := fmt.Sprintf("epoch-%02d", index)
		if err := store.CreateSession(ctx, Session{ID: sessionID, Title: sessionID, State: "READY", PreferredBackend: "codex-a", CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatalf("CreateSession(%s) error = %v", sessionID, err)
		}
		if err := store.CreateSessionEpoch(ctx, SessionEpoch{ID: epochID, SessionID: sessionID, Sequence: 1, Backend: "codex-a", IdentityID: "identity-a", State: "STARTING", StartedAt: now}); err != nil {
			t.Fatalf("CreateSessionEpoch(%s) error = %v", epochID, err)
		}
	}
	vmids := make(chan int, count)
	errorsFound := make(chan error, count)
	var workers sync.WaitGroup
	for index := 0; index < count; index++ {
		workers.Add(1)
		go func(index int) {
			defer workers.Done()
			sessionID := fmt.Sprintf("session-%02d", index)
			epochID := fmt.Sprintf("epoch-%02d", index)
			binding, err := store.AllocateSessionRuntimeBinding(ctx, SessionRuntimeBinding{ID: "binding-" + sessionID, SessionID: sessionID, EpochID: epochID, Generation: "gen-1", State: "ALLOCATING", CreatedAt: now, UpdatedAt: now})
			if err != nil {
				errorsFound <- err
				return
			}
			vmids <- binding.VMID
		}(index)
	}
	workers.Wait()
	close(vmids)
	close(errorsFound)
	for err := range errorsFound {
		t.Errorf("AllocateSessionRuntimeBinding() error = %v", err)
	}
	seen := make(map[int]bool)
	for vmid := range vmids {
		if seen[vmid] {
			t.Errorf("duplicate allocated VMID %d", vmid)
		}
		seen[vmid] = true
	}
	if len(seen) != count {
		t.Fatalf("allocated %d unique VMIDs, want %d", len(seen), count)
	}
}

func TestSessionRuntimeBindingCannotReuseVMIDOrCrossLinkSessionEpoch(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "sessions.sqlite"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Now().UTC()
	for _, id := range []string{"session-1", "session-2"} {
		if err := store.CreateSession(ctx, Session{ID: id, Title: id, State: "READY", PreferredBackend: "codex-a", CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatalf("CreateSession(%s) error = %v", id, err)
		}
	}
	for _, epoch := range []SessionEpoch{
		{ID: "epoch-1", SessionID: "session-1", Sequence: 1, Backend: "codex-a", IdentityID: "identity-a", State: "STARTING", StartedAt: now},
		{ID: "epoch-2", SessionID: "session-2", Sequence: 1, Backend: "codex-a", IdentityID: "identity-a", State: "STARTING", StartedAt: now},
	} {
		if err := store.CreateSessionEpoch(ctx, epoch); err != nil {
			t.Fatalf("CreateSessionEpoch(%s) error = %v", epoch.ID, err)
		}
	}
	first := SessionRuntimeBinding{ID: "binding-1", SessionID: "session-1", EpochID: "epoch-1", VMID: 4000, Generation: "gen-1", State: "ALLOCATING", CreatedAt: now, UpdatedAt: now}
	if err := store.CreateSessionRuntimeBinding(ctx, first); err != nil {
		t.Fatalf("CreateSessionRuntimeBinding(first) error = %v", err)
	}
	if _, err := store.db.Exec("UPDATE session_runtime_bindings SET vmid = 3990 WHERE id = 'binding-1'"); err == nil {
		t.Fatal("direct SQL accepted a VMID outside the dedicated Session range")
	}
	if err := store.CreateSessionRuntimeBinding(ctx, SessionRuntimeBinding{ID: "binding-2", SessionID: "session-2", EpochID: "epoch-2", VMID: 4000, Generation: "gen-2", State: "ALLOCATING", CreatedAt: now, UpdatedAt: now}); err == nil {
		t.Fatal("CreateSessionRuntimeBinding() reused an already reserved VMID")
	}
	if err := store.CreateSessionRuntimeBinding(ctx, SessionRuntimeBinding{ID: "binding-cross", SessionID: "session-1", EpochID: "epoch-2", VMID: 4001, Generation: "gen-3", State: "ALLOCATING", CreatedAt: now, UpdatedAt: now}); err == nil {
		t.Fatal("CreateSessionRuntimeBinding() cross-linked a different session's epoch")
	}
	if err := store.CreateSessionRuntimeBinding(ctx, SessionRuntimeBinding{ID: "binding-outside", SessionID: "session-1", EpochID: "epoch-1", VMID: 3900, Generation: "gen-4", State: "ALLOCATING", CreatedAt: now, UpdatedAt: now}); err == nil {
		t.Fatal("CreateSessionRuntimeBinding() accepted a VMID outside the dedicated Session range")
	}
	for _, transition := range [][2]string{{"ALLOCATING", "CREATING"}, {"CREATING", "STARTING"}, {"STARTING", "READY"}, {"READY", "STOPPING"}, {"STOPPING", "STOPPED"}, {"STOPPED", "DELETING"}, {"DELETING", "DELETED"}} {
		if err := store.UpdateSessionRuntimeBindingState(ctx, first.ID, transition[0], transition[1], "", now.Add(time.Second)); err != nil {
			t.Fatalf("UpdateSessionRuntimeBindingState(%s -> %s) error = %v", transition[0], transition[1], err)
		}
	}
	if err := store.CreateSessionRuntimeBinding(ctx, SessionRuntimeBinding{ID: "binding-reused", SessionID: "session-2", EpochID: "epoch-2", VMID: 4000, Generation: "gen-5", State: "ALLOCATING", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("CreateSessionRuntimeBinding() could not reuse confirmed-deleted VMID: %v", err)
	}
	current, err := store.GetSessionRuntimeBindingByVMID(ctx, 4000)
	if err != nil {
		t.Fatalf("GetSessionRuntimeBindingByVMID() error = %v", err)
	}
	if current.ID != "binding-reused" {
		t.Fatalf("GetSessionRuntimeBindingByVMID() = %q, want current binding", current.ID)
	}
	var historyVMID int
	if err := store.db.QueryRow("SELECT vmid FROM session_runtime_binding_history WHERE runtime_binding_id = ? ORDER BY id DESC LIMIT 1", first.ID).Scan(&historyVMID); err != nil {
		t.Fatalf("read deleted runtime history: %v", err)
	}
	if historyVMID != first.VMID {
		t.Fatalf("archived VMID = %d, want %d", historyVMID, first.VMID)
	}
	if _, err := store.db.Exec("UPDATE session_runtime_binding_history SET final_state = 'STOPPED' WHERE runtime_binding_id = ?", first.ID); err == nil {
		t.Fatal("runtime binding history update succeeded, want immutable audit row")
	}
	if _, err := store.db.Exec("DELETE FROM session_runtime_binding_history WHERE runtime_binding_id = ?", first.ID); err == nil {
		t.Fatal("runtime binding history delete succeeded, want immutable audit row")
	}
}

func TestCompleteSessionRuntimeReplacementKeepsVMIDReserved(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "sessions.sqlite"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Now().UTC()
	for _, sessionID := range []string{"session-1", "session-2"} {
		if err := store.CreateSession(ctx, Session{ID: sessionID, Title: sessionID, State: "READY", PreferredBackend: "codex-a", CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatalf("CreateSession(%s) error = %v", sessionID, err)
		}
	}
	for _, epoch := range []SessionEpoch{
		{ID: "epoch-1", SessionID: "session-1", Sequence: 1, Backend: "codex-a", IdentityID: "identity-a", State: "STARTING", StartedAt: now},
		{ID: "epoch-2", SessionID: "session-2", Sequence: 1, Backend: "codex-a", IdentityID: "identity-a", State: "STARTING", StartedAt: now},
	} {
		if err := store.CreateSessionEpoch(ctx, epoch); err != nil {
			t.Fatalf("CreateSessionEpoch(%s) error = %v", epoch.ID, err)
		}
	}
	binding, err := store.AllocateSessionRuntimeBinding(ctx, SessionRuntimeBinding{ID: "binding-1", SessionID: "session-1", EpochID: "epoch-1", Generation: "gen-1", State: "ALLOCATING", CreatedAt: now, UpdatedAt: now})
	if err != nil {
		t.Fatalf("AllocateSessionRuntimeBinding() error = %v", err)
	}
	for _, transition := range [][2]string{{"ALLOCATING", "CREATING"}, {"CREATING", "STARTING"}, {"STARTING", "READY"}, {"READY", "STOPPING"}, {"STOPPING", "STOPPED"}, {"STOPPED", "DELETING"}} {
		if err := store.UpdateSessionRuntimeBindingState(ctx, binding.ID, transition[0], transition[1], "", now.Add(time.Second)); err != nil {
			t.Fatalf("UpdateSessionRuntimeBindingState(%s -> %s) error = %v", transition[0], transition[1], err)
		}
	}
	if err := store.CompleteSessionRuntimeReplacement(ctx, binding.ID, "gen-2", now.Add(time.Minute)); err != nil {
		t.Fatalf("CompleteSessionRuntimeReplacement() error = %v", err)
	}
	replacement, err := store.GetSessionRuntimeBinding(ctx, "session-1", "epoch-1")
	if err != nil {
		t.Fatalf("GetSessionRuntimeBinding() error = %v", err)
	}
	if replacement.VMID != binding.VMID || replacement.Generation != "gen-2" || replacement.State != "ALLOCATING" || replacement.PendingOperation != "create" {
		t.Fatalf("replacement = %+v, want same VMID held for new generation", replacement)
	}
	other, err := store.AllocateSessionRuntimeBinding(ctx, SessionRuntimeBinding{ID: "binding-2", SessionID: "session-2", EpochID: "epoch-2", Generation: "gen-1", State: "ALLOCATING", CreatedAt: now, UpdatedAt: now})
	if err != nil {
		t.Fatalf("allocate concurrent Session runtime: %v", err)
	}
	if other.VMID == binding.VMID {
		t.Fatalf("concurrent allocation reused replacement VMID %d", binding.VMID)
	}
	history, err := store.ListSessionRuntimeBindingHistory(ctx, "session-1", "epoch-1")
	if err != nil || len(history) != 1 || history[0].Generation != "gen-1" || history[0].VMID != binding.VMID {
		t.Fatalf("replacement history = %+v, %v; want archived old generation on same VMID", history, err)
	}
}

func TestMigrationV8PreservesExistingSessionRuntimeBinding(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "sessions-v7.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	db.SetMaxOpenConns(1)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx() error = %v", err)
	}
	if _, err := tx.ExecContext(ctx, "CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY)"); err != nil {
		t.Fatalf("create migration ledger: %v", err)
	}
	for _, migration := range schemaMigrations[:len(schemaMigrations)-1] {
		for _, statement := range migration.Statements {
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				t.Fatalf("apply migration %d: %v", migration.Version, err)
			}
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO schema_migrations(version) VALUES (?)", migration.Version); err != nil {
			t.Fatalf("record migration %d: %v", migration.Version, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit v7 schema: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO sessions (id, title, state, preferred_backend, created_at, updated_at)
		VALUES ('session-v7', 'preserve', 'READY', 'codex-a', 'now', 'now')`); err != nil {
		t.Fatalf("insert v7 session: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO session_epochs (id, session_id, sequence, backend, identity_id, state)
		VALUES ('epoch-v7', 'session-v7', 1, 'codex-a', 'identity-a', 'ACTIVE')`); err != nil {
		t.Fatalf("insert v7 epoch: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO session_runtime_bindings (id, session_id, epoch_id, runtime_id, vmid, generation, state, created_at, updated_at)
		VALUES ('binding-v7', 'session-v7', 'epoch-v7', 'lxc-4007', 4007, 'generation-v7', 'READY', '2026-09-30T10:00:00Z', '2026-09-30T10:00:00Z')`); err != nil {
		t.Fatalf("insert v7 runtime binding: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close v7 database: %v", err)
	}

	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open() migrates v7 database: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	got, err := store.GetSessionRuntimeBinding(ctx, "session-v7", "epoch-v7")
	if err != nil {
		t.Fatalf("GetSessionRuntimeBinding() error = %v", err)
	}
	if got.ID != "binding-v7" || got.VMID != 4007 || got.RuntimeID != "lxc-4007" || got.Generation != "generation-v7" || got.State != "READY" || got.PendingOperation != "" {
		t.Fatalf("migrated binding = %+v, want original v7 state preserved", got)
	}
	if err := store.RequireTable(ctx, "session_runtime_binding_history"); err != nil {
		t.Fatalf("v8 history table missing: %v", err)
	}
}
