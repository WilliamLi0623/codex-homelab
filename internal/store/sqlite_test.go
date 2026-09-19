package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/WilliamLi0623/codex-homelab/internal/domain"
)

func TestOpenMigratesV3ControllerSchema(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "controller.sqlite"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	for _, table := range []string{
		"repositories", "task_intake", "runs", "tasks", "task_attempts",
		"execution_handles", "capacity_nodes", "codex_threads", "task_messages",
		"task_events", "model_profiles", "model_attempts", "provider_health",
		"git_refs", "validation_results", "leases", "commands", "schema_migrations",
	} {
		if err := store.RequireTable(context.Background(), table); err != nil {
			t.Errorf("RequireTable(%q) error = %v", table, err)
		}
	}
}

func TestCreateTaskReturnsExistingTaskForSameIdempotencyKey(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "controller.sqlite"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	first, created, err := store.CreateTask(context.Background(), domain.NewTask("task-1", "owner/repository", "main", "Fix the failing tests", "request-1"))
	if err != nil || !created {
		t.Fatalf("first CreateTask() = (%+v, %t, %v), want created task", first, created, err)
	}
	second, created, err := store.CreateTask(context.Background(), domain.NewTask("task-2", "owner/repository", "main", "Fix the failing tests", "request-1"))
	if err != nil || created {
		t.Fatalf("second CreateTask() = (%+v, %t, %v), want existing task", second, created, err)
	}
	if second.ID != first.ID {
		t.Fatalf("second task ID = %q, want %q", second.ID, first.ID)
	}
}

func TestCreateTaskRejectsIdempotencyKeyForDifferentRequest(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "controller.sqlite"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	_, _, err = store.CreateTask(context.Background(), domain.NewTask("task-1", "owner/repository", "main", "Fix the failing tests", "request-1"))
	if err != nil {
		t.Fatalf("first CreateTask() error = %v", err)
	}
	_, _, err = store.CreateTask(context.Background(), domain.NewTask("task-2", "owner/repository", "main", "Change the database", "request-1"))
	if !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("CreateTask() error = %v, want ErrIdempotencyConflict", err)
	}
}
