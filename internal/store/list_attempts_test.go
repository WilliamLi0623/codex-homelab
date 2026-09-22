package store

import (
	"context"
	"testing"

	"github.com/WilliamLi0623/codex-homelab/internal/domain"
)

func TestListAttemptsReturnsDurableAttemptsInOrder(t *testing.T) {
	database := newTestStore(t)
	task := domain.NewTask("task-list-attempts", "owner/repository", "main", "objective", "idempotency-list-attempts")
	if _, _, err := database.CreateTask(context.Background(), task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	insertAttempt(t, database, "attempt-2", task.ID, 2, domain.AttemptCreated)
	insertAttempt(t, database, "attempt-1", task.ID, 1, domain.AttemptCreated)
	attempts, err := database.ListAttempts(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("ListAttempts() error = %v", err)
	}
	if len(attempts) != 2 || attempts[0].ID != "attempt-1" || attempts[1].ID != "attempt-2" {
		t.Fatalf("attempts = %+v, want ordered attempts", attempts)
	}
}
