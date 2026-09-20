package store

import (
	"context"
	"errors"
	"testing"

	"github.com/WilliamLi0623/codex-homelab/internal/domain"
)

func TestAttemptExecutionSpecIsIdempotentAndConflictSafe(t *testing.T) {
	s := openCapacityTestStore(t)
	task, _, err := s.CreateTask(context.Background(), domain.NewTask("task", "owner/repo", "main", "objective", "execution-spec-request"))
	if err != nil {
		t.Fatal(err)
	}
	_, attempt, err := s.StartAttempt(context.Background(), task.ID, "openai-primary")
	if err != nil {
		t.Fatal(err)
	}
	spec := AttemptExecutionSpec{TaskID: task.ID, AttemptID: attempt.ID, Branch: "refs/heads/codex/task/attempt", ValidationCommand: []string{"go", "test", "./..."}}
	first, created, err := s.EnsureAttemptExecutionSpec(context.Background(), spec)
	if err != nil || !created {
		t.Fatalf("first spec = (%+v, %t, %v)", first, created, err)
	}
	second, created, err := s.EnsureAttemptExecutionSpec(context.Background(), spec)
	if err != nil || created || second.ID != first.ID {
		t.Fatalf("second spec = (%+v, %t, %v)", second, created, err)
	}
	spec.Branch = "refs/heads/other"
	if _, _, err := s.EnsureAttemptExecutionSpec(context.Background(), spec); !errors.Is(err, ErrExecutionSpecConflict) {
		t.Fatalf("conflict = %v", err)
	}
	got, err := s.GetAttemptExecutionSpec(context.Background(), task.ID, attempt.ID)
	if err != nil || len(got.ValidationCommand) != 3 {
		t.Fatalf("loaded spec = (%+v, %v)", got, err)
	}
}

func TestAttemptExecutionSpecRejectsEmptyValidation(t *testing.T) {
	s := openCapacityTestStore(t)
	_, _, err := s.EnsureAttemptExecutionSpec(context.Background(), AttemptExecutionSpec{TaskID: "task", AttemptID: "attempt", Branch: "main"})
	if !errors.Is(err, ErrExecutionSpecInvalid) {
		t.Fatalf("error = %v", err)
	}
}
