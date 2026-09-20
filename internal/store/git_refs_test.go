package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/WilliamLi0623/codex-homelab/internal/domain"
)

const testCommitSHA = "0123456789abcdef0123456789abcdef01234567"

func TestRecordGitRefRequiresOwnedAttemptAndStrictCommitSHA(t *testing.T) {
	s := openGitRefStore(t)
	ctx := context.Background()
	_, _, err := s.CreateTask(ctx, domain.NewTask("task-1", "owner/repo", "main", "change", "request-1"))
	if err != nil {
		t.Fatal(err)
	}
	_, attempt, err := s.StartAttempt(ctx, "task-1", "profile")
	if err != nil {
		t.Fatal(err)
	}

	if _, _, err := s.RecordGitRef(ctx, "task-1", "missing", "refs/heads/change", testCommitSHA, "LOCAL"); !errors.Is(err, ErrAttemptNotFound) {
		t.Fatalf("RecordGitRef(missing attempt) error = %v, want ErrAttemptNotFound", err)
	}
	for _, sha := range []string{"", "0123456789abcdef0123456789abcdef0123456", "0123456789abcdef0123456789abcdef012345678", "0123456789abcdef0123456789abcdef0123456g"} {
		if _, _, err := s.RecordGitRef(ctx, "task-1", attempt.ID, "refs/heads/change", sha, "LOCAL"); err == nil {
			t.Fatalf("RecordGitRef(sha %q) succeeded, want strict SHA validation", sha)
		}
	}
}

func TestRecordGitRefIsIdempotentForBindingButRejectsConflicts(t *testing.T) {
	s := openGitRefStore(t)
	ctx := context.Background()
	createGitRefFixture(t, s, "task-1", "request-1")
	_, attempt, err := s.StartAttempt(ctx, "task-1", "profile")
	if err != nil {
		t.Fatal(err)
	}

	first, created, err := s.RecordGitRef(ctx, "task-1", attempt.ID, "refs/heads/change", testCommitSHA, "LOCAL")
	if err != nil || !created {
		t.Fatalf("first RecordGitRef() = (%+v, %t, %v), want created", first, created, err)
	}
	second, created, err := s.RecordGitRef(ctx, "task-1", attempt.ID, "refs/heads/change", testCommitSHA, "LOCAL")
	if err != nil || created || second.ID != first.ID {
		t.Fatalf("repeat RecordGitRef() = (%+v, %t, %v), want same binding idempotently", second, created, err)
	}
	if _, _, err := s.RecordGitRef(ctx, "task-1", attempt.ID, "refs/heads/other", testCommitSHA, "LOCAL"); !errors.Is(err, ErrGitRefConflict) {
		t.Fatalf("branch conflict error = %v, want ErrGitRefConflict", err)
	}
	if _, _, err := s.RecordGitRef(ctx, "task-1", attempt.ID, "refs/heads/change", "fedcba9876543210fedcba9876543210fedcba98", "LOCAL"); !errors.Is(err, ErrGitRefConflict) {
		t.Fatalf("SHA conflict error = %v, want ErrGitRefConflict", err)
	}
}

func TestRecordGitRefDoesNotOverwriteAnotherAttempt(t *testing.T) {
	s := openGitRefStore(t)
	ctx := context.Background()
	createGitRefFixture(t, s, "task-1", "request-1")
	_, firstAttempt, err := s.StartAttempt(ctx, "task-1", "profile")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, "UPDATE task_attempts SET state = ? WHERE id = ?", domain.AttemptCancelled, firstAttempt.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, "UPDATE tasks SET state = ? WHERE id = ?", domain.TaskFailed, "task-1"); err != nil {
		t.Fatal(err)
	}
	_, secondAttempt, err := s.RetryTask(ctx, "task-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.RecordGitRef(ctx, "task-1", firstAttempt.ID, "refs/heads/change", testCommitSHA, "LOCAL"); err != nil {
		t.Fatal(err)
	}
	second, created, err := s.RecordGitRef(ctx, "task-1", secondAttempt.ID, "refs/heads/change", testCommitSHA, "LOCAL")
	if err != nil || !created || second.AttemptID != secondAttempt.ID {
		t.Fatalf("second attempt RecordGitRef() = (%+v, %t, %v), want independent record", second, created, err)
	}
}

func openGitRefStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "controller.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func createGitRefFixture(t *testing.T, s *Store, taskID, requestID string) {
	t.Helper()
	if _, _, err := s.CreateTask(context.Background(), domain.NewTask(taskID, "owner/repo", "main", "change", requestID)); err != nil {
		t.Fatal(err)
	}
}
