package store

import (
	"context"
	"testing"

	"github.com/WilliamLi0623/codex-homelab/internal/domain"
)

func TestStartAttemptCreatesInitialAttemptAndMovesTaskToPlanned(t *testing.T) {
	database := newTestStore(t)
	task := domain.NewTask("task-start", "owner/repo", "main", "make a change", "idem-start")
	if _, _, err := database.CreateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	planned, attempt, err := database.StartAttempt(context.Background(), task.ID, "openai-primary")
	if err != nil {
		t.Fatal(err)
	}
	if planned.State != domain.TaskPlanned || attempt.Number != 1 || attempt.State != domain.AttemptCreated {
		t.Fatalf("task=%+v attempt=%+v", planned, attempt)
	}
	if _, _, err := database.StartAttempt(context.Background(), task.ID, "openai-primary"); err == nil {
		t.Fatal("second StartAttempt() succeeded for an active attempt")
	}
}
