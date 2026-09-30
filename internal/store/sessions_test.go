package store

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestSessionAndEpochSurviveStoreRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "sessions.sqlite")
	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	session := Session{ID: "session-1", Title: "Repo work", State: "READY", PreferredBackend: "codex-a", CreatedAt: now, UpdatedAt: now}
	if err := store.CreateSession(ctx, session); err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	if err := store.SetSessionWorkspaceVolume(ctx, session.ID, "pool:subvol-4000-disk-1"); err != nil {
		t.Fatalf("SetSessionWorkspaceVolume() error = %v", err)
	}
	if err := store.SetSessionWorkspaceVolume(ctx, session.ID, "pool:subvol-4000-disk-2"); err == nil {
		t.Fatal("SetSessionWorkspaceVolume() replaced the existing durable workspace volume")
	}
	epoch := SessionEpoch{ID: "epoch-1", SessionID: session.ID, Sequence: 1, Backend: "codex-a", IdentityID: "identity-a", State: "ACTIVE", StartedAt: now}
	if err := store.CreateSessionEpoch(ctx, epoch); err != nil {
		t.Fatalf("CreateSessionEpoch() error = %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	store, err = Open(path)
	if err != nil {
		t.Fatalf("reopen Store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	gotSession, err := store.GetSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("GetSession() error = %v", err)
	}
	if gotSession.ID != session.ID || gotSession.Title != session.Title || gotSession.PreferredBackend != session.PreferredBackend || gotSession.WorkspaceVolumeID != "pool:subvol-4000-disk-1" {
		t.Fatalf("GetSession() = %+v, want %+v", gotSession, session)
	}
	epochs, err := store.ListSessionEpochs(ctx, session.ID)
	if err != nil {
		t.Fatalf("ListSessionEpochs() error = %v", err)
	}
	if len(epochs) != 1 || epochs[0].ID != epoch.ID || epochs[0].State != epoch.State || epochs[0].IdentityID != epoch.IdentityID {
		t.Fatalf("ListSessionEpochs() = %+v, want persisted epoch %+v", epochs, epoch)
	}
}

func TestCreateSessionRejectsInvalidIdentityAndEpochUniqueness(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "sessions.sqlite"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Now().UTC()
	if err := store.CreateSession(ctx, Session{ID: "session-1", Title: "Repo work", State: "READY", PreferredBackend: "codex-a", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	if err := store.CreateSessionEpoch(ctx, SessionEpoch{ID: "epoch-1", SessionID: "session-1", Sequence: 1, Backend: "codex-a", IdentityID: "identity-a", State: "ACTIVE", StartedAt: now}); err != nil {
		t.Fatalf("CreateSessionEpoch() error = %v", err)
	}
	if err := store.CreateSessionEpoch(ctx, SessionEpoch{ID: "epoch-2", SessionID: "session-1", Sequence: 2, Backend: "codex-b", IdentityID: "identity-b", State: "ACTIVE", StartedAt: now}); err == nil {
		t.Fatal("CreateSessionEpoch() accepted a second active epoch for one session")
	}
	if err := store.CreateSession(ctx, Session{ID: "session-2", Title: "", State: "READY", PreferredBackend: "codex-a", CreatedAt: now, UpdatedAt: now}); err == nil {
		t.Fatal("CreateSession() accepted an empty title")
	}
	if err := store.CreateSession(ctx, Session{ID: "session-3", Title: "Repo work", State: "MAGIC", PreferredBackend: "codex-a", CreatedAt: now, UpdatedAt: now}); err == nil {
		t.Fatal("CreateSession() accepted an unsupported lifecycle state")
	}
}

func TestConcurrentStoresCannotCreateTwoActiveEpochs(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "sessions.sqlite")
	first, err := Open(path)
	if err != nil {
		t.Fatalf("Open(first) error = %v", err)
	}
	t.Cleanup(func() { _ = first.Close() })
	second, err := Open(path)
	if err != nil {
		t.Fatalf("Open(second) error = %v", err)
	}
	t.Cleanup(func() { _ = second.Close() })
	now := time.Now().UTC()
	if err := first.CreateSession(ctx, Session{ID: "session-1", Title: "Repo work", State: "READY", PreferredBackend: "codex-a", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}

	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for i, store := range []*Store{first, second} {
		wg.Add(1)
		go func(i int, store *Store) {
			defer wg.Done()
			<-start
			id := []string{"epoch-1", "epoch-2"}[i]
			errs <- store.CreateSessionEpoch(ctx, SessionEpoch{ID: id, SessionID: "session-1", Sequence: i + 1, Backend: "codex-a", IdentityID: "identity-a", State: "ACTIVE", StartedAt: now})
		}(i, store)
	}
	close(start)
	wg.Wait()
	close(errs)

	succeeded := 0
	for err := range errs {
		if err == nil {
			succeeded++
		}
	}
	if succeeded != 1 {
		t.Fatalf("concurrent CreateSessionEpoch successes = %d, want exactly one", succeeded)
	}
	var active int
	if err := first.db.QueryRow(`SELECT COUNT(*) FROM session_epochs WHERE session_id = 'session-1' AND state = 'ACTIVE'`).Scan(&active); err != nil {
		t.Fatalf("count active epochs: %v", err)
	}
	if active != 1 {
		t.Fatalf("active epochs after concurrent writes = %d, want 1", active)
	}
}
