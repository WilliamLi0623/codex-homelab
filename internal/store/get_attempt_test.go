package store

import (
	"context"
	"errors"
	"testing"

	"github.com/WilliamLi0623/codex-homelab/internal/domain"
)

func TestGetAttemptReturnsExistingAttempt(t *testing.T) {
	database := newTestStore(t)
	task := failedTask(t, database, "task-1")
	insertAttempt(t, database, "attempt-1", task.ID, 1, domain.AttemptCompleted)

	attempt, err := database.GetAttempt(context.Background(), task.ID, "attempt-1")
	if err != nil {
		t.Fatalf("GetAttempt() error = %v", err)
	}
	if attempt.ID != "attempt-1" || attempt.TaskID != task.ID || attempt.Number != 1 || attempt.State != domain.AttemptCompleted {
		t.Fatalf("GetAttempt() = %+v, want stored attempt", attempt)
	}
}

func TestGetAttemptReturnsNotFoundForUnknownAttempt(t *testing.T) {
	database := newTestStore(t)
	task := failedTask(t, database, "task-1")

	_, err := database.GetAttempt(context.Background(), task.ID, "missing")
	if !errors.Is(err, ErrAttemptNotFound) {
		t.Fatalf("GetAttempt() error = %v, want ErrAttemptNotFound", err)
	}
}

func TestGetAttemptReturnsNotFoundForAttemptOwnedByAnotherTask(t *testing.T) {
	database := newTestStore(t)
	firstTask := failedTask(t, database, "task-1")
	secondTask := failedTask(t, database, "task-2")
	insertAttempt(t, database, "attempt-1", firstTask.ID, 1, domain.AttemptCompleted)

	_, err := database.GetAttempt(context.Background(), secondTask.ID, "attempt-1")
	if !errors.Is(err, ErrAttemptNotFound) {
		t.Fatalf("GetAttempt() error = %v, want ErrAttemptNotFound", err)
	}
}
