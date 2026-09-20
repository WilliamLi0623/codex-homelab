package store

import (
	"context"
	"errors"
	"testing"

	"github.com/WilliamLi0623/codex-homelab/internal/domain"
)

func TestFinalizeAttemptSuccessCatchesUpAndIsIdempotent(t *testing.T) {
	s := newTestStore(t)
	created := domain.NewTask("finalize-task", "owner/repo", "main", "change", "finalize-request")
	task, _, err := s.CreateTask(context.Background(), created)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.StartAttempt(context.Background(), task.ID, "openai-primary"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(context.Background(), "UPDATE task_attempts SET id = ? WHERE task_id = ?", "finalize-attempt", task.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.FinalizeAttemptSuccess(context.Background(), task.ID, "finalize-attempt"); err != nil {
		t.Fatalf("FinalizeAttemptSuccess() error = %v", err)
	}
	gotTask, err := s.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	gotAttempt, err := s.GetAttempt(context.Background(), task.ID, "finalize-attempt")
	if err != nil {
		t.Fatal(err)
	}
	if gotTask.State != domain.TaskSucceeded || gotAttempt.State != domain.AttemptCompleted {
		t.Fatalf("states = task %s attempt %s", gotTask.State, gotAttempt.State)
	}
	if err := s.FinalizeAttemptSuccess(context.Background(), task.ID, "finalize-attempt"); err != nil {
		t.Fatalf("idempotent finalization error = %v", err)
	}
}

func TestFinalizeAttemptSuccessRejectsUnknownAndTerminalFailures(t *testing.T) {
	s := newTestStore(t)
	task := failedTask(t, s, "finalize-reject")
	insertAttempt(t, s, "unknown-attempt", task.ID, 1, domain.AttemptUnknown)
	if err := s.FinalizeAttemptSuccess(context.Background(), task.ID, "unknown-attempt"); !errors.Is(err, ErrAttemptFinalizationRejected) {
		t.Fatalf("UNKNOWN error = %v", err)
	}
	insertAttempt(t, s, "failed-attempt", task.ID, 2, domain.AttemptExecutionFailed)
	if err := s.FinalizeAttemptSuccess(context.Background(), task.ID, "failed-attempt"); !errors.Is(err, ErrAttemptFinalizationRejected) {
		t.Fatalf("failed attempt error = %v", err)
	}
}
