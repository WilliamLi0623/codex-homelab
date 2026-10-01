package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestSessionBootstrapPersistsOrderedStagesAcrossReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "bootstrap.sqlite")
	s := newBootstrapTestStore(t, path, "bootstrap-generation-1")

	for i, stage := range sessionBootstrapStages {
		got, claimed, err := s.BeginSessionBootstrapStage(ctx, "binding-1", "bootstrap-generation-1", stage)
		if err != nil || !claimed || got.Status != SessionBootstrapIntent {
			t.Fatalf("BeginSessionBootstrapStage(%q) = (%+v, %t, %v), want newly claimed intent", stage, got, claimed, err)
		}
		if i == 0 {
			if _, _, err := s.BeginSessionBootstrapStage(ctx, "binding-1", "bootstrap-generation-1", SessionBootstrapGuestIdentity); !errors.Is(err, ErrSessionBootstrapConflict) {
				t.Fatalf("BeginSessionBootstrapStage(guest_identity before isolation completion) error = %v, want conflict", err)
			}
			if err := s.Close(); err != nil {
				t.Fatalf("Close() error = %v", err)
			}
			s, err = Open(path)
			if err != nil {
				t.Fatalf("reopen Store: %v", err)
			}
			t.Cleanup(func() { _ = s.Close() })
			got, claimed, err = s.BeginSessionBootstrapStage(ctx, "binding-1", "bootstrap-generation-1", stage)
			if err != nil || claimed || got.Status != SessionBootstrapIntent {
				t.Fatalf("replayed BeginSessionBootstrapStage(%q) = (%+v, %t, %v), want persisted intent without claim", stage, got, claimed, err)
			}
		}
		if err := s.CompleteSessionBootstrapStage(ctx, "binding-1", "bootstrap-generation-1", stage, SessionBootstrapIntent, SessionBootstrapEvidence{SHA256: bootstrapTestDigest}); err != nil {
			t.Fatalf("CompleteSessionBootstrapStage(%q): %v", stage, err)
		}
	}

	got, err := s.GetSessionBootstrapStage(ctx, "binding-1", "bootstrap-generation-1", SessionBootstrapNetworkEnabled)
	if err != nil || got.Status != SessionBootstrapComplete || got.Evidence.SHA256 != bootstrapTestDigest {
		t.Fatalf("GetSessionBootstrapStage(network_enabled) = (%+v, %v), want persisted complete evidence", got, err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close() after completion: %v", err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatalf("reopen Store after completion: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	got, err = s.GetSessionBootstrapStage(ctx, "binding-1", "bootstrap-generation-1", SessionBootstrapNetworkEnabled)
	if err != nil || got.Status != SessionBootstrapComplete || got.Evidence.SHA256 != bootstrapTestDigest {
		t.Fatalf("reopened complete evidence = (%+v, %v), want persisted digest", got, err)
	}
}

func TestSessionBootstrapV12AddsImageBackupStageAndPreservesCompletedEvidence(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "bootstrap-v11.sqlite")
	legacy, err := openPreBootstrapStore(t, path)
	if err != nil {
		t.Fatalf("open migration 9 Store: %v", err)
	}
	for _, migration := range schemaMigrations {
		if migration.Version < 10 || migration.Version > 11 {
			continue
		}
		for _, statement := range migration.Statements {
			if _, err := legacy.db.ExecContext(ctx, statement); err != nil {
				t.Fatalf("apply legacy migration %d: %v", migration.Version, err)
			}
		}
		if _, err := legacy.db.ExecContext(ctx, "INSERT INTO schema_migrations(version) VALUES (?)", migration.Version); err != nil {
			t.Fatalf("record legacy migration %d: %v", migration.Version, err)
		}
	}
	now := time.Now().UTC()
	if err := legacy.CreateSession(ctx, Session{ID: "session-1", Title: "Bootstrap", State: "READY", PreferredBackend: "codex-a", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("CreateSession(): %v", err)
	}
	if err := legacy.CreateSessionEpoch(ctx, SessionEpoch{ID: "epoch-1", SessionID: "session-1", Sequence: 1, Backend: "codex-a", IdentityID: "account-a", State: "ACTIVE", StartedAt: now}); err != nil {
		t.Fatalf("CreateSessionEpoch(): %v", err)
	}
	if err := legacy.CreateSessionRuntimeBinding(ctx, SessionRuntimeBinding{ID: "binding-1", SessionID: "session-1", EpochID: "epoch-1", VMID: 4010, Generation: "bootstrap-generation-1", State: "ALLOCATING", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("CreateSessionRuntimeBinding(): %v", err)
	}
	if _, err := legacy.db.ExecContext(ctx, "INSERT INTO session_bootstrap_checkpoints (runtime_binding_id, generation, stage, status, evidence_sha256) VALUES (?, ?, ?, 'COMPLETE', ?)", "binding-1", "bootstrap-generation-1", SessionBootstrapHostPin, bootstrapTestDigest); err != nil {
		t.Fatalf("insert prior completed evidence: %v", err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("close migration 11 Store: %v", err)
	}

	migrated, err := Open(path)
	if err != nil {
		t.Fatalf("Open() migration 12: %v", err)
	}
	t.Cleanup(func() { _ = migrated.Close() })
	preserved, err := migrated.GetSessionBootstrapStage(ctx, "binding-1", "bootstrap-generation-1", SessionBootstrapHostPin)
	if err != nil || preserved.Status != SessionBootstrapComplete || preserved.Evidence.SHA256 != bootstrapTestDigest {
		t.Fatalf("preserved host pin = (%+v, %v), want original COMPLETE evidence", preserved, err)
	}
	backup, claimed, err := migrated.BeginSessionBootstrapStage(ctx, "binding-1", "bootstrap-generation-1", SessionBootstrapImageBackup)
	if err != nil || !claimed || backup.Status != SessionBootstrapIntent {
		t.Fatalf("claim image backup = (%+v, %t, %v), want new INTENT", backup, claimed, err)
	}
	if _, _, err := migrated.BeginSessionBootstrapStage(ctx, "binding-1", "bootstrap-generation-1", SessionBootstrapImageSanitized); !errors.Is(err, ErrSessionBootstrapConflict) {
		t.Fatalf("claim sanitation before backup completion = %v, want conflict", err)
	}
	if err := migrated.CompleteSessionBootstrapStage(ctx, "binding-1", "bootstrap-generation-1", SessionBootstrapImageBackup, SessionBootstrapIntent, SessionBootstrapEvidence{SHA256: bootstrapTestDigest}); err != nil {
		t.Fatalf("complete image backup: %v", err)
	}
	if _, claimed, err := migrated.BeginSessionBootstrapStage(ctx, "binding-1", "bootstrap-generation-1", SessionBootstrapImageSanitized); err != nil || !claimed {
		t.Fatalf("claim sanitation after backup completion = claimed %t, err %v", claimed, err)
	}
}

func TestSessionBootstrapBeginNeverReplaysExistingIntentUnknownOrComplete(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name   string
		status SessionBootstrapStatus
	}{
		{name: "intent", status: SessionBootstrapIntent},
		{name: "unknown", status: SessionBootstrapUnknown},
		{name: "complete", status: SessionBootstrapComplete},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newBootstrapTestStore(t, filepath.Join(t.TempDir(), "bootstrap.sqlite"), "bootstrap-generation-1")
			first, claimed, err := s.BeginSessionBootstrapStage(ctx, "binding-1", "bootstrap-generation-1", SessionBootstrapIsolation)
			if err != nil || !claimed {
				t.Fatalf("initial BeginSessionBootstrapStage() = (%+v, %t, %v), want claim", first, claimed, err)
			}
			switch tc.status {
			case SessionBootstrapUnknown:
				if err := s.MarkSessionBootstrapStageUnknown(ctx, "binding-1", "bootstrap-generation-1", SessionBootstrapIsolation, SessionBootstrapIntent); err != nil {
					t.Fatalf("MarkSessionBootstrapStageUnknown(): %v", err)
				}
			case SessionBootstrapComplete:
				if err := s.CompleteSessionBootstrapStage(ctx, "binding-1", "bootstrap-generation-1", SessionBootstrapIsolation, SessionBootstrapIntent, SessionBootstrapEvidence{SHA256: bootstrapTestDigest}); err != nil {
					t.Fatalf("CompleteSessionBootstrapStage(): %v", err)
				}
			}
			got, claimed, err := s.BeginSessionBootstrapStage(ctx, "binding-1", "bootstrap-generation-1", SessionBootstrapIsolation)
			if err != nil || claimed || got.Status != tc.status {
				t.Fatalf("replayed BeginSessionBootstrapStage() = (%+v, %t, %v), want existing %s without claim", got, claimed, err, tc.status)
			}
		})
	}
}

func TestSessionBootstrapConcurrentBeginHasOneWinner(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "bootstrap.sqlite")
	left := newBootstrapTestStore(t, path, "bootstrap-generation-1")
	right, err := Open(path)
	if err != nil {
		t.Fatalf("Open(second Store): %v", err)
	}
	t.Cleanup(func() { _ = right.Close() })
	start := make(chan struct{})
	claimed := make(chan bool, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, candidate := range []*Store{left, right} {
		wg.Add(1)
		go func(candidate *Store) {
			defer wg.Done()
			<-start
			_, newlyClaimed, err := candidate.BeginSessionBootstrapStage(ctx, "binding-1", "bootstrap-generation-1", SessionBootstrapIsolation)
			claimed <- newlyClaimed
			errs <- err
		}(candidate)
	}
	close(start)
	wg.Wait()
	close(claimed)
	close(errs)
	wins := 0
	for got := range claimed {
		if got {
			wins++
		}
	}
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent BeginSessionBootstrapStage() error = %v", err)
		}
	}
	if wins != 1 {
		t.Fatalf("newly claimed concurrent Begin calls = %d, want exactly one", wins)
	}
}

func TestSessionBootstrapUsesGenerationCASAndKeepsReplacedEvidence(t *testing.T) {
	ctx := context.Background()
	s := newBootstrapTestStore(t, filepath.Join(t.TempDir(), "bootstrap.sqlite"), "bootstrap-generation-1")
	if _, claimed, err := s.BeginSessionBootstrapStage(ctx, "binding-1", "bootstrap-generation-1", SessionBootstrapIsolation); err != nil || !claimed {
		t.Fatalf("BeginSessionBootstrapStage() = (%t, %v), want claim", claimed, err)
	}
	if err := s.MarkSessionBootstrapStageUnknown(ctx, "binding-1", "bootstrap-generation-1", SessionBootstrapIsolation, SessionBootstrapIntent); err != nil {
		t.Fatalf("MarkSessionBootstrapStageUnknown(): %v", err)
	}
	if err := s.CompleteSessionBootstrapStage(ctx, "binding-1", "bootstrap-generation-1", SessionBootstrapIsolation, SessionBootstrapIntent, SessionBootstrapEvidence{SHA256: bootstrapTestDigest}); !errors.Is(err, ErrSessionBootstrapConflict) {
		t.Fatalf("completion with stale expected status error = %v, want conflict", err)
	}
	if err := s.CompleteSessionBootstrapStage(ctx, "binding-1", "bootstrap-generation-1", SessionBootstrapIsolation, SessionBootstrapUnknown, SessionBootstrapEvidence{SHA256: bootstrapTestDigest}); err != nil {
		t.Fatalf("positive readback resolution: %v", err)
	}
	if err := s.CompleteSessionBootstrapStage(ctx, "binding-1", "bootstrap-generation-1", SessionBootstrapIsolation, SessionBootstrapIntent, SessionBootstrapEvidence{SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}); !errors.Is(err, ErrSessionBootstrapConflict) {
		t.Fatalf("attempt to replace completed evidence error = %v, want conflict", err)
	}
	completed, err := s.GetSessionBootstrapStage(ctx, "binding-1", "bootstrap-generation-1", SessionBootstrapIsolation)
	if err != nil || completed.Evidence.SHA256 != bootstrapTestDigest {
		t.Fatalf("completed evidence after rejected replacement = (%+v, %v), want original digest", completed, err)
	}

	if err := s.UpdateSessionRuntimeBindingState(ctx, "binding-1", "ALLOCATING", "UNKNOWN", "", time.Now().UTC()); err != nil {
		t.Fatalf("mark binding unknown: %v", err)
	}
	if err := s.UpdateSessionRuntimeBindingState(ctx, "binding-1", "UNKNOWN", "DELETING", "", time.Now().UTC()); err != nil {
		t.Fatalf("mark binding deleting: %v", err)
	}
	if err := s.CompleteSessionRuntimeReplacement(ctx, "binding-1", "bootstrap-generation-2", time.Now().UTC()); err != nil {
		t.Fatalf("replace binding generation: %v", err)
	}
	if _, _, err := s.BeginSessionBootstrapStage(ctx, "binding-1", "bootstrap-generation-1", SessionBootstrapGuestIdentity); !errors.Is(err, ErrSessionBootstrapConflict) {
		t.Fatalf("BeginSessionBootstrapStage(old generation) error = %v, want conflict", err)
	}
	if err := s.CompleteSessionBootstrapStage(ctx, "binding-1", "bootstrap-generation-1", SessionBootstrapIsolation, SessionBootstrapUnknown, SessionBootstrapEvidence{SHA256: bootstrapTestDigest}); !errors.Is(err, ErrSessionBootstrapConflict) {
		t.Fatalf("CompleteSessionBootstrapStage(old generation) error = %v, want conflict", err)
	}
	old, err := s.GetSessionBootstrapStage(ctx, "binding-1", "bootstrap-generation-1", SessionBootstrapIsolation)
	if err != nil || old.Status != SessionBootstrapComplete || old.Evidence.SHA256 != bootstrapTestDigest {
		t.Fatalf("old generation evidence = (%+v, %v), want retained completion", old, err)
	}
	if _, claimed, err := s.BeginSessionBootstrapStage(ctx, "binding-1", "bootstrap-generation-2", SessionBootstrapIsolation); err != nil || !claimed {
		t.Fatalf("BeginSessionBootstrapStage(new generation) = (%t, %v), want isolated claim", claimed, err)
	}
}

func TestSessionBootstrapRejectsInvalidStageDigestAndStatus(t *testing.T) {
	ctx := context.Background()
	s := newBootstrapTestStore(t, filepath.Join(t.TempDir(), "bootstrap.sqlite"), "bootstrap-generation-1")
	if _, _, err := s.BeginSessionBootstrapStage(ctx, "binding-1", "bootstrap-generation-1", "account_provisioned"); !errors.Is(err, ErrSessionBootstrapInvalid) {
		t.Fatalf("BeginSessionBootstrapStage(invalid stage) error = %v, want invalid", err)
	}
	if _, claimed, err := s.BeginSessionBootstrapStage(ctx, "binding-1", "bootstrap-generation-1", SessionBootstrapIsolation); err != nil || !claimed {
		t.Fatalf("BeginSessionBootstrapStage() = (%t, %v), want claim", claimed, err)
	}
	for _, digest := range []string{"", "ABCDEF", "not-a-digest", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaG", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa "} {
		if err := s.CompleteSessionBootstrapStage(ctx, "binding-1", "bootstrap-generation-1", SessionBootstrapIsolation, SessionBootstrapIntent, SessionBootstrapEvidence{SHA256: digest}); !errors.Is(err, ErrSessionBootstrapInvalid) {
			t.Errorf("CompleteSessionBootstrapStage(digest %q) error = %v, want invalid", digest, err)
		}
	}
	if err := s.CompleteSessionBootstrapStage(ctx, "binding-1", "bootstrap-generation-1", SessionBootstrapIsolation, SessionBootstrapComplete, SessionBootstrapEvidence{SHA256: bootstrapTestDigest}); !errors.Is(err, ErrSessionBootstrapInvalid) {
		t.Errorf("CompleteSessionBootstrapStage(expected COMPLETE) error = %v, want invalid", err)
	}
	if err := s.MarkSessionBootstrapStageUnknown(ctx, "binding-1", "bootstrap-generation-1", SessionBootstrapIsolation, SessionBootstrapComplete); !errors.Is(err, ErrSessionBootstrapInvalid) {
		t.Errorf("MarkSessionBootstrapStageUnknown(expected COMPLETE) error = %v, want invalid", err)
	}
}

func TestSessionBootstrapRawSQLRejectsEvidenceOnNoncompleteRows(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		status   SessionBootstrapStatus
		evidence string
	}{
		{status: SessionBootstrapIntent, evidence: "synthetic-private-evidence-fixture"},
		{status: SessionBootstrapIntent, evidence: bootstrapTestDigest},
		{status: SessionBootstrapUnknown, evidence: "synthetic-private-evidence-fixture"},
		{status: SessionBootstrapUnknown, evidence: bootstrapTestDigest},
	} {
		t.Run(string(tc.status)+"/"+tc.evidence[:min(len(tc.evidence), 10)], func(t *testing.T) {
			s := newBootstrapTestStore(t, filepath.Join(t.TempDir(), "bootstrap.sqlite"), "bootstrap-generation-1")
			if _, claimed, err := s.BeginSessionBootstrapStage(ctx, "binding-1", "bootstrap-generation-1", SessionBootstrapIsolation); err != nil || !claimed {
				t.Fatalf("BeginSessionBootstrapStage() = (%t, %v), want claim", claimed, err)
			}
			if tc.status == SessionBootstrapUnknown {
				if err := s.MarkSessionBootstrapStageUnknown(ctx, "binding-1", "bootstrap-generation-1", SessionBootstrapIsolation, SessionBootstrapIntent); err != nil {
					t.Fatalf("MarkSessionBootstrapStageUnknown(): %v", err)
				}
			}
			_, err := s.db.ExecContext(ctx, `UPDATE session_bootstrap_checkpoints SET evidence_sha256 = ? WHERE runtime_binding_id = 'binding-1' AND generation = 'bootstrap-generation-1' AND stage = 'isolation'`, tc.evidence)
			if err == nil {
				t.Fatalf("raw SQL stored evidence %q on %s row", tc.evidence, tc.status)
			}
		})
	}
}

func TestSessionBootstrapRawSQLCannotRewriteDowngradeOrDeleteCompleteEvidence(t *testing.T) {
	ctx := context.Background()
	mutations := []struct {
		name string
		sql  string
	}{
		{name: "rewrite digest", sql: `UPDATE session_bootstrap_checkpoints SET evidence_sha256 = 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa' WHERE runtime_binding_id = 'binding-1' AND generation = 'bootstrap-generation-1' AND stage = 'isolation'`},
		{name: "downgrade status", sql: `UPDATE session_bootstrap_checkpoints SET status = 'INTENT', evidence_sha256 = '' WHERE runtime_binding_id = 'binding-1' AND generation = 'bootstrap-generation-1' AND stage = 'isolation'`},
		{name: "delete completed evidence", sql: `DELETE FROM session_bootstrap_checkpoints WHERE runtime_binding_id = 'binding-1' AND generation = 'bootstrap-generation-1' AND stage = 'isolation'`},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			s := newBootstrapTestStore(t, filepath.Join(t.TempDir(), "bootstrap.sqlite"), "bootstrap-generation-1")
			if _, claimed, err := s.BeginSessionBootstrapStage(ctx, "binding-1", "bootstrap-generation-1", SessionBootstrapIsolation); err != nil || !claimed {
				t.Fatalf("BeginSessionBootstrapStage() = (%t, %v), want claim", claimed, err)
			}
			if err := s.CompleteSessionBootstrapStage(ctx, "binding-1", "bootstrap-generation-1", SessionBootstrapIsolation, SessionBootstrapIntent, SessionBootstrapEvidence{SHA256: bootstrapTestDigest}); err != nil {
				t.Fatalf("CompleteSessionBootstrapStage(): %v", err)
			}
			if _, err := s.db.ExecContext(ctx, mutation.sql); err == nil {
				t.Fatalf("raw SQL mutation %q changed completed evidence", mutation.name)
			}
			got, err := s.GetSessionBootstrapStage(ctx, "binding-1", "bootstrap-generation-1", SessionBootstrapIsolation)
			if err != nil || got.Status != SessionBootstrapComplete || got.Evidence.SHA256 != bootstrapTestDigest {
				t.Fatalf("checkpoint after rejected SQL mutation = (%+v, %v), want immutable completion", got, err)
			}
		})
	}
}

func TestSessionBootstrapReplaceCannotEraseCompletedEvidenceAcrossRecursiveTriggerModes(t *testing.T) {
	ctx := context.Background()
	for _, mode := range []struct {
		name  string
		value string
		set   bool
	}{
		{name: "default"},
		{name: "explicit off", value: "OFF", set: true},
		{name: "explicit on", value: "ON", set: true},
	} {
		t.Run(mode.name, func(t *testing.T) {
			s := newBootstrapTestStore(t, filepath.Join(t.TempDir(), "bootstrap.sqlite"), "bootstrap-generation-1")
			if _, claimed, err := s.BeginSessionBootstrapStage(ctx, "binding-1", "bootstrap-generation-1", SessionBootstrapIsolation); err != nil || !claimed {
				t.Fatalf("BeginSessionBootstrapStage() = (%t, %v), want claim", claimed, err)
			}
			if err := s.CompleteSessionBootstrapStage(ctx, "binding-1", "bootstrap-generation-1", SessionBootstrapIsolation, SessionBootstrapIntent, SessionBootstrapEvidence{SHA256: bootstrapTestDigest}); err != nil {
				t.Fatalf("CompleteSessionBootstrapStage(): %v", err)
			}

			conn, err := s.db.Conn(ctx)
			if err != nil {
				t.Fatalf("pin SQLite connection: %v", err)
			}
			defer conn.Close()
			if mode.set {
				if _, err := conn.ExecContext(ctx, "PRAGMA recursive_triggers = "+mode.value); err != nil {
					t.Fatalf("set recursive_triggers %s: %v", mode.value, err)
				}
			}
			var recursiveTriggers int
			if err := conn.QueryRowContext(ctx, "PRAGMA recursive_triggers").Scan(&recursiveTriggers); err != nil {
				t.Fatalf("read recursive_triggers: %v", err)
			}
			if mode.name == "default" && recursiveTriggers != 0 {
				t.Fatalf("SQLite recursive_triggers default = %d, expected OFF for this probe", recursiveTriggers)
			}
			if mode.set {
				want := 0
				if mode.value == "ON" {
					want = 1
				}
				if recursiveTriggers != want {
					t.Fatalf("recursive_triggers = %d after setting %s, want %d", recursiveTriggers, mode.value, want)
				}
			}

			_, err = conn.ExecContext(ctx, "INSERT OR REPLACE INTO session_bootstrap_checkpoints (runtime_binding_id, generation, stage, status, evidence_sha256) VALUES (?, ?, ?, 'COMPLETE', ?)", "binding-1", "bootstrap-generation-1", SessionBootstrapIsolation, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
			if err == nil {
				t.Fatalf("INSERT OR REPLACE succeeded with recursive_triggers=%d", recursiveTriggers)
			}
			var status, digest string
			if err := conn.QueryRowContext(ctx, "SELECT status, evidence_sha256 FROM session_bootstrap_checkpoints WHERE runtime_binding_id = ? AND generation = ? AND stage = ?", "binding-1", "bootstrap-generation-1", SessionBootstrapIsolation).Scan(&status, &digest); err != nil {
				t.Fatalf("read checkpoint after REPLACE: %v", err)
			}
			if status != "COMPLETE" || digest != bootstrapTestDigest {
				t.Fatalf("checkpoint after rejected REPLACE = (%s, %s), want original completion", status, digest)
			}
		})
	}
}

func TestSessionBootstrapRejectsMissingDeletedAndWrongGenerationBinding(t *testing.T) {
	ctx := context.Background()
	s := newBootstrapTestStore(t, filepath.Join(t.TempDir(), "bootstrap.sqlite"), "bootstrap-generation-1")
	for _, tc := range []struct {
		name       string
		bindingID  string
		generation string
		wantErr    error
	}{
		{name: "missing binding", bindingID: "missing", generation: "bootstrap-generation-1", wantErr: ErrSessionBootstrapNotFound},
		{name: "wrong generation", bindingID: "binding-1", generation: "stale-generation", wantErr: ErrSessionBootstrapConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := s.BeginSessionBootstrapStage(ctx, tc.bindingID, tc.generation, SessionBootstrapIsolation); !errors.Is(err, tc.wantErr) {
				t.Fatalf("BeginSessionBootstrapStage() error = %v, want %v", err, tc.wantErr)
			}
		})
	}
	if err := s.UpdateSessionRuntimeBindingState(ctx, "binding-1", "ALLOCATING", "UNKNOWN", "", time.Now().UTC()); err != nil {
		t.Fatalf("mark binding unknown: %v", err)
	}
	if err := s.UpdateSessionRuntimeBindingState(ctx, "binding-1", "UNKNOWN", "DELETING", "", time.Now().UTC()); err != nil {
		t.Fatalf("mark binding deleting: %v", err)
	}
	if err := s.UpdateSessionRuntimeBindingState(ctx, "binding-1", "DELETING", "DELETED", "", time.Now().UTC()); err != nil {
		t.Fatalf("mark binding deleted: %v", err)
	}
	if _, _, err := s.BeginSessionBootstrapStage(ctx, "binding-1", "bootstrap-generation-1", SessionBootstrapIsolation); !errors.Is(err, ErrSessionBootstrapConflict) {
		t.Fatalf("BeginSessionBootstrapStage(deleted binding) error = %v, want conflict", err)
	}
}

func TestSessionBootstrapMigrationPreservesExistingRuntimeBinding(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "pre-bootstrap.sqlite")
	db, err := openPreBootstrapStore(t, path)
	if err != nil {
		t.Fatalf("open pre-bootstrap Store: %v", err)
	}
	now := time.Now().UTC()
	if err := db.CreateSession(ctx, Session{ID: "session-1", Title: "Bootstrap", State: "READY", PreferredBackend: "codex-a", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("CreateSession(): %v", err)
	}
	if err := db.CreateSessionEpoch(ctx, SessionEpoch{ID: "epoch-1", SessionID: "session-1", Sequence: 1, Backend: "codex-a", IdentityID: "identity-a", State: "ACTIVE", StartedAt: now}); err != nil {
		t.Fatalf("CreateSessionEpoch(): %v", err)
	}
	if err := db.CreateSessionRuntimeBinding(ctx, SessionRuntimeBinding{ID: "binding-1", SessionID: "session-1", EpochID: "epoch-1", VMID: 4010, Generation: "existing-generation", State: "ALLOCATING", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("CreateSessionRuntimeBinding(): %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close pre-bootstrap Store: %v", err)
	}

	migrated, err := Open(path)
	if err != nil {
		t.Fatalf("Open() migration error = %v", err)
	}
	t.Cleanup(func() { _ = migrated.Close() })
	got, err := migrated.GetSessionRuntimeBinding(ctx, "session-1", "epoch-1")
	if err != nil || got.ID != "binding-1" || got.Generation != "existing-generation" || got.VMID != 4010 {
		t.Fatalf("binding after bootstrap migration = (%+v, %v), want preserved binding", got, err)
	}
}

func TestSessionBootstrapV10MigrationScrubsNoncompleteEvidenceAndPreservesCompletion(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "bootstrap-v9.sqlite")
	legacy, err := openPreBootstrapStore(t, path)
	if err != nil {
		t.Fatalf("open migration 9 Store: %v", err)
	}
	now := time.Now().UTC()
	if err := legacy.CreateSession(ctx, Session{ID: "session-1", Title: "Bootstrap", State: "READY", PreferredBackend: "codex-a", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("CreateSession(): %v", err)
	}
	if err := legacy.CreateSessionEpoch(ctx, SessionEpoch{ID: "epoch-1", SessionID: "session-1", Sequence: 1, Backend: "codex-a", IdentityID: "identity-a", State: "ACTIVE", StartedAt: now}); err != nil {
		t.Fatalf("CreateSessionEpoch(): %v", err)
	}
	if err := legacy.CreateSessionRuntimeBinding(ctx, SessionRuntimeBinding{ID: "binding-1", SessionID: "session-1", EpochID: "epoch-1", VMID: 4010, Generation: "bootstrap-generation-1", State: "ALLOCATING", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("CreateSessionRuntimeBinding(): %v", err)
	}
	for _, row := range []struct {
		stage    SessionBootstrapStage
		status   SessionBootstrapStatus
		evidence string
	}{
		{stage: SessionBootstrapIsolation, status: SessionBootstrapIntent, evidence: "synthetic-private-evidence-fixture"},
		{stage: SessionBootstrapGuestIdentity, status: SessionBootstrapUnknown, evidence: bootstrapTestDigest},
		{stage: SessionBootstrapHostPin, status: SessionBootstrapComplete, evidence: bootstrapTestDigest},
	} {
		if _, err := legacy.db.ExecContext(ctx, "INSERT INTO session_bootstrap_checkpoints (runtime_binding_id, generation, stage, status, evidence_sha256) VALUES (?, ?, ?, ?, ?)", "binding-1", "bootstrap-generation-1", row.stage, row.status, row.evidence); err != nil {
			t.Fatalf("insert legacy %s row: %v", row.status, err)
		}
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("close migration 9 Store: %v", err)
	}

	upgraded, err := Open(path)
	if err != nil {
		t.Fatalf("Open() migration 10 error = %v", err)
	}
	t.Cleanup(func() { _ = upgraded.Close() })
	for _, stage := range []SessionBootstrapStage{SessionBootstrapIsolation, SessionBootstrapGuestIdentity} {
		got, err := upgraded.GetSessionBootstrapStage(ctx, "binding-1", "bootstrap-generation-1", stage)
		if err != nil || got.Evidence.SHA256 != "" {
			t.Errorf("migration 10 %s evidence = (%+v, %v), want scrubbed evidence", stage, got, err)
		}
	}
	complete, err := upgraded.GetSessionBootstrapStage(ctx, "binding-1", "bootstrap-generation-1", SessionBootstrapHostPin)
	if err != nil || complete.Status != SessionBootstrapComplete || complete.Evidence.SHA256 != bootstrapTestDigest {
		t.Errorf("migration 10 complete checkpoint = (%+v, %v), want retained digest", complete, err)
	}
}

const bootstrapTestDigest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func newBootstrapTestStore(t *testing.T, path, generation string) *Store {
	t.Helper()
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	now := time.Now().UTC()
	ctx := context.Background()
	if err := s.CreateSession(ctx, Session{ID: "session-1", Title: "Bootstrap", State: "READY", PreferredBackend: "codex-a", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("CreateSession(): %v", err)
	}
	if err := s.CreateSessionEpoch(ctx, SessionEpoch{ID: "epoch-1", SessionID: "session-1", Sequence: 1, Backend: "codex-a", IdentityID: "identity-a", State: "ACTIVE", StartedAt: now}); err != nil {
		t.Fatalf("CreateSessionEpoch(): %v", err)
	}
	if err := s.CreateSessionRuntimeBinding(ctx, SessionRuntimeBinding{ID: "binding-1", SessionID: "session-1", EpochID: "epoch-1", VMID: 4010, Generation: generation, State: "ALLOCATING", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("CreateSessionRuntimeBinding(): %v", err)
	}
	return s
}

func openPreBootstrapStore(t *testing.T, path string) (*Store, error) {
	t.Helper()
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout%3d5000&_pragma=foreign_keys%3d1")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if _, err := db.Exec(`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY)`); err != nil {
		_ = db.Close()
		return nil, err
	}
	for _, migration := range schemaMigrations {
		if migration.Version > 9 {
			break
		}
		for _, statement := range migration.Statements {
			if _, err := db.Exec(statement); err != nil {
				_ = db.Close()
				return nil, err
			}
		}
		if _, err := db.Exec(`INSERT INTO schema_migrations(version) VALUES (?)`, migration.Version); err != nil {
			_ = db.Close()
			return nil, err
		}
	}
	return &Store{db: db, reservedVMIDs: map[int]struct{}{3900: {}}}, nil
}
