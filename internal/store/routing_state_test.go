package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/WilliamLi0623/codex-homelab/internal/domain"
	_ "modernc.org/sqlite"
)

func TestRoutingStateIsMonotonicAndIdempotent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	state := RoutingState{Mode: "normal", ObservedAt: time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC), Generation: 7}
	if err := s.SetRoutingState(ctx, state); err != nil {
		t.Fatalf("SetRoutingState() error = %v", err)
	}
	if err := s.SetRoutingState(ctx, state); err != nil {
		t.Fatalf("idempotent SetRoutingState() error = %v", err)
	}

	for _, stale := range []RoutingState{
		{Mode: "quota_fallback", ObservedAt: state.ObservedAt.Add(time.Second), Generation: 6},
		{Mode: "quota_fallback", ObservedAt: state.ObservedAt.Add(-time.Second), Generation: 7},
		{Mode: "quota_fallback", ObservedAt: state.ObservedAt.Add(-time.Second), Generation: 8},
	} {
		if err := s.SetRoutingState(ctx, stale); !errors.Is(err, ErrStaleRoutingState) {
			t.Fatalf("SetRoutingState(%+v) error = %v, want ErrStaleRoutingState", stale, err)
		}
	}

	got, err := s.GetRoutingState(ctx)
	if err != nil {
		t.Fatalf("GetRoutingState() error = %v", err)
	}
	if got != state {
		t.Fatalf("GetRoutingState() = %+v, want %+v", got, state)
	}
}

func TestRoutingStateRejectsInvalidModeAndFirstReadIsAbsent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if _, err := s.GetRoutingState(ctx); !errors.Is(err, ErrRoutingStateNotFound) {
		t.Fatalf("GetRoutingState() error = %v, want ErrRoutingStateNotFound", err)
	}
	if err := s.SetRoutingState(ctx, RoutingState{Mode: "provider_down", ObservedAt: time.Now().UTC(), Generation: 1}); !errors.Is(err, ErrInvalidRoutingState) {
		t.Fatalf("SetRoutingState() error = %v, want ErrInvalidRoutingState", err)
	}
}

func TestRoutingStateConcurrentUpdatesKeepHighestGeneration(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	const workers = 12
	var wg sync.WaitGroup
	var errorsMu sync.Mutex
	var updateErrors []error
	for generation := 1; generation <= workers; generation++ {
		generation := generation
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.SetRoutingState(ctx, RoutingState{
				Mode:       "normal",
				ObservedAt: time.Unix(int64(generation), 0).UTC(),
				Generation: int64(generation),
			}); err != nil && !errors.Is(err, ErrStaleRoutingState) {
				errorsMu.Lock()
				updateErrors = append(updateErrors, err)
				errorsMu.Unlock()
			}
		}()
	}
	wg.Wait()
	if len(updateErrors) != 0 {
		t.Fatalf("unexpected concurrent update errors: %v", updateErrors)
	}
	got, err := s.GetRoutingState(ctx)
	if err != nil {
		t.Fatalf("GetRoutingState() error = %v", err)
	}
	if got.Generation != workers {
		t.Fatalf("final generation = %d, want %d", got.Generation, workers)
	}
}

func TestStartAttemptWithRoutePersistsAtomicImmutableSnapshot(t *testing.T) {
	s := newTestStore(t)
	task := domain.NewTask("task-route", "owner/repo", "main", "objective", "idem-route")
	if _, _, err := s.CreateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	route := testAttemptRoute()
	_, attempt, err := s.StartAttemptWithRoute(context.Background(), task.ID, "worker", route)
	if err != nil {
		t.Fatalf("StartAttemptWithRoute() error = %v", err)
	}
	if attempt.ModelProfile != "worker" {
		t.Fatalf("attempt profile = %q, want fixed role marker %q", attempt.ModelProfile, "worker")
	}
	got, err := s.GetAttemptRoute(context.Background(), task.ID, attempt.ID)
	if err != nil {
		t.Fatalf("GetAttemptRoute() error = %v", err)
	}
	if got != route {
		t.Fatalf("route = %+v, want %+v", got, route)
	}
	if err := s.ReplaceAttemptRoute(context.Background(), task.ID, attempt.ID, testAttemptRoute()); !errors.Is(err, ErrAttemptRouteImmutable) {
		t.Fatalf("ReplaceAttemptRoute() error = %v, want ErrAttemptRouteImmutable", err)
	}
	if _, err := s.db.Exec("UPDATE task_attempts SET route_model = ? WHERE id = ?", "other-model", attempt.ID); err == nil {
		t.Fatal("direct route snapshot UPDATE succeeded")
	}
}

func TestStartAttemptWithInvalidRouteDoesNotCreateAttempt(t *testing.T) {
	s := newTestStore(t)
	task := domain.NewTask("task-invalid-route", "owner/repo", "main", "objective", "idem-invalid-route")
	if _, _, err := s.CreateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	route := testAttemptRoute()
	route.SecretKey = "Bearer secret-value"
	if _, _, err := s.StartAttemptWithRoute(context.Background(), task.ID, "worker", route); !errors.Is(err, ErrInvalidRoutingState) {
		t.Fatalf("StartAttemptWithRoute() error = %v, want ErrInvalidRoutingState", err)
	}
	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM task_attempts WHERE task_id = ?", task.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("attempt count = %d, want 0", count)
	}
}

func TestLegacyAttemptHasNoFabricatedRoute(t *testing.T) {
	s := newTestStore(t)
	task := domain.NewTask("task-legacy-route", "owner/repo", "main", "objective", "idem-legacy-route")
	if _, _, err := s.CreateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	_, attempt, err := s.StartAttempt(context.Background(), task.ID, "openai-primary")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetAttemptRoute(context.Background(), task.ID, attempt.ID); !errors.Is(err, ErrAttemptRouteNotFound) {
		t.Fatalf("GetAttemptRoute() error = %v, want ErrAttemptRouteNotFound", err)
	}
}

func TestRetryTaskWithRoutePersistsNewImmutableSnapshot(t *testing.T) {
	s := newTestStore(t)
	task := failedTask(t, s, "task-retry-route")
	insertAttempt(t, s, "attempt-retry-route", task.ID, 1, domain.AttemptExecutionFailed)
	route := testAttemptRoute()
	_, retry, err := s.RetryTaskWithRoute(context.Background(), task.ID, route)
	if err != nil {
		t.Fatalf("RetryTaskWithRoute() error = %v", err)
	}
	if retry.ModelProfile != "worker" {
		t.Fatalf("retry profile = %q, want fixed role marker %q", retry.ModelProfile, "worker")
	}
	got, err := s.GetAttemptRoute(context.Background(), task.ID, retry.ID)
	if err != nil {
		t.Fatalf("GetAttemptRoute() error = %v", err)
	}
	if got != route {
		t.Fatalf("retry route = %+v, want %+v", got, route)
	}
}

func TestMigrationV6PreservesV5Data(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "v5.sqlite")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	if _, err := db.Exec("CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY)"); err != nil {
		t.Fatal(err)
	}
	for _, migration := range schemaMigrations[:len(schemaMigrations)-1] {
		for _, statement := range migration.Statements {
			if _, err := db.Exec(statement); err != nil {
				t.Fatalf("apply v5 migration %d: %v", migration.Version, err)
			}
		}
		if _, err := db.Exec("INSERT INTO schema_migrations(version) VALUES (?)", migration.Version); err != nil {
			t.Fatalf("record v5 migration %d: %v", migration.Version, err)
		}
	}
	if _, err := db.Exec(`INSERT INTO tasks(id, repository_id, base_ref, objective, execution_class, state, created_at) VALUES ('legacy-task', 'repo', 'main', 'legacy', 'dedicated-lxc', 'RECEIVED', '2026-09-23T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO task_attempts(id, task_id, attempt_number, model_profile, state, created_at) VALUES ('legacy-attempt', 'legacy-task', 1, 'openai-primary', 'UNKNOWN', '2026-09-23T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open(v5) error = %v", err)
	}
	defer s.Close()
	var objective string
	if err := s.db.QueryRow("SELECT objective FROM tasks WHERE id = 'legacy-task'").Scan(&objective); err != nil {
		t.Fatal(err)
	}
	if objective != "legacy" {
		t.Fatalf("legacy objective = %q, want legacy", objective)
	}
	if _, err := s.GetAttemptRoute(context.Background(), "legacy-task", "legacy-attempt"); !errors.Is(err, ErrAttemptRouteNotFound) {
		t.Fatalf("legacy route error = %v, want ErrAttemptRouteNotFound", err)
	}
}

func testAttemptRoute() AttemptRouteSnapshot {
	return AttemptRouteSnapshot{
		Mode: "normal", Generation: 7, Role: "worker", Provider: "openai",
		Model: "gpt-6-luna", WireAPI: "responses", ReasoningEffort: "high",
		BaseURL: "https://api.openai.com/v1", SecretName: "openai-model-gateway", SecretKey: "api-key",
	}
}
