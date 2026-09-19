package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/WilliamLi0623/codex-homelab/internal/domain"
)

func TestReconcileUnknownAttemptRecordsTerminalOutcome(t *testing.T) {
	database := newTestStore(t)
	task := failedTask(t, database, "task-1")
	insertAttempt(t, database, "attempt-1", task.ID, 1, domain.AttemptUnknown)

	attempt, err := database.ReconcileUnknownAttempt(context.Background(), task.ID, "attempt-1", domain.AttemptExecutionFailed)
	if err != nil {
		t.Fatalf("ReconcileUnknownAttempt() error = %v", err)
	}
	if attempt.State != domain.AttemptExecutionFailed {
		t.Fatalf("state = %s, want EXECUTION_FAILED", attempt.State)
	}

	events, err := database.ListTaskEvents(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("ListTaskEvents() error = %v", err)
	}
	if got := events[len(events)-1].Type; got != "attempt.reconciled" {
		t.Fatalf("last event = %q, want attempt.reconciled", got)
	}
}

func TestRetryRejectsUnknownLatestAttemptUntilReconciled(t *testing.T) {
	database := newTestStore(t)
	task := failedTask(t, database, "task-1")
	insertAttempt(t, database, "attempt-1", task.ID, 1, domain.AttemptUnknown)

	if _, _, err := database.RetryTask(context.Background(), task.ID); !errors.Is(err, ErrAttemptRequiresReconciliation) {
		t.Fatalf("RetryTask() error = %v, want ErrAttemptRequiresReconciliation", err)
	}
	if _, err := database.ReconcileUnknownAttempt(context.Background(), task.ID, "attempt-1", domain.AttemptExecutionFailed); err != nil {
		t.Fatalf("ReconcileUnknownAttempt() error = %v", err)
	}
	if _, attempt, err := database.RetryTask(context.Background(), task.ID); err != nil || attempt.Number != 2 {
		t.Fatalf("RetryTask() = (%+v, %v), want second attempt", attempt, err)
	}
}

func newTestStore(t *testing.T) *Store {
	t.Helper()
	database, err := Open(filepath.Join(t.TempDir(), "controller.sqlite"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}

func failedTask(t *testing.T, database *Store, id string) domain.Task {
	t.Helper()
	task := domain.NewTask(id, "owner/repository", "main", "objective", "idempotency-"+id)
	if err := task.TransitionTo(domain.TaskFailed); err != nil {
		t.Fatalf("TransitionTo(FAILED) error = %v", err)
	}
	if _, _, err := database.CreateTask(context.Background(), task); err != nil {
		t.Fatalf("CreateTask() error = %v", err)
	}
	return task
}

func insertAttempt(t *testing.T, database *Store, id, taskID string, number int, state domain.AttemptState) {
	t.Helper()
	_, err := database.db.Exec("INSERT INTO task_attempts(id, task_id, attempt_number, model_profile, state, created_at) VALUES (?, ?, ?, ?, ?, ?)", id, taskID, number, "openai-primary", state, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		t.Fatalf("insert attempt: %v", err)
	}
}
