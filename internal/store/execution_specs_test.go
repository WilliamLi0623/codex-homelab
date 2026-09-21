package store

import (
	"context"
	"errors"
	"testing"
	"time"

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

func TestListPendingAttemptExecutionSpecsExcludesDurableCompletion(t *testing.T) {
	s := openCapacityTestStore(t)
	task, _, err := s.CreateTask(context.Background(), domain.NewTask("task", "owner/repo", "main", "objective", "pending-spec-request"))
	if err != nil {
		t.Fatal(err)
	}
	_, attempt, err := s.StartAttempt(context.Background(), task.ID, "openai-primary")
	if err != nil {
		t.Fatal(err)
	}
	spec := AttemptExecutionSpec{TaskID: task.ID, AttemptID: attempt.ID, Branch: "refs/heads/codex/task/attempt", ValidationCommand: []string{"go", "test"}}
	if _, _, err := s.EnsureAttemptExecutionSpec(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	pending, err := s.ListPendingAttemptExecutionSpecs(context.Background())
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending = (%+v, %v)", pending, err)
	}
	if _, err := s.RecordValidationResult(context.Background(), ValidationResult{ID: "completion-" + attempt.ID, AttemptID: attempt.ID, Command: "go test", State: "PASSED", CreatedAt: time.Now().UTC()}, ""); err != nil {
		t.Fatal(err)
	}
	pending, err = s.ListPendingAttemptExecutionSpecs(context.Background())
	if err != nil || len(pending) != 0 {
		t.Fatalf("pending after completion = (%+v, %v)", pending, err)
	}
}

func TestListPendingAttemptExecutionSpecsRequeuesIncompleteRelease(t *testing.T) {
	s := openCapacityTestStore(t)
	task, _, err := s.CreateTask(context.Background(), domain.NewTask("task", "owner/repo", "main", "objective", "release-retry-spec-request"))
	if err != nil {
		t.Fatal(err)
	}
	_, attempt, err := s.StartAttempt(context.Background(), task.ID, "glm-5.3-flash")
	if err != nil {
		t.Fatal(err)
	}
	spec := AttemptExecutionSpec{TaskID: task.ID, AttemptID: attempt.ID, Branch: "refs/heads/codex/task/release-retry", ValidationCommand: []string{"go", "test"}}
	if _, _, err := s.EnsureAttemptExecutionSpec(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecordValidationResult(context.Background(), ValidationResult{ID: "completion-" + attempt.ID, AttemptID: attempt.ID, Command: "go test", State: "PASSED", CreatedAt: time.Now().UTC()}, ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.EnsureReleaseProgress(context.Background(), ReleaseProgressRequest{TaskID: task.ID, AttemptID: attempt.ID, VMID: 3010, Generation: "gen", KubeNode: "node"}); err != nil {
		t.Fatal(err)
	}
	pending, err := s.ListPendingAttemptExecutionSpecs(context.Background())
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending with incomplete release = (%+v, %v)", pending, err)
	}
	if err := s.UpdateReleaseProgress(context.Background(), task.ID, attempt.ID, ReleaseStepDone, ReleaseStateCompleted, ""); err != nil {
		t.Fatal(err)
	}
	pending, err = s.ListPendingAttemptExecutionSpecs(context.Background())
	if err != nil || len(pending) != 0 {
		t.Fatalf("pending after release completion = (%+v, %v)", pending, err)
	}
}
