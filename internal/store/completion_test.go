package store

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestRecordAttemptCompletionAtomicallyPersistsSafeCompletion(t *testing.T) {
	s := newTestStore(t)
	task := failedTask(t, s, "completion-task")
	insertAttempt(t, s, "completion-attempt", task.ID, 1, "RUNNING")
	want := CompletionRecord{ID: "completion-1", TaskID: task.ID, AttemptID: "completion-attempt", Command: "go test ./...", ValidationState: "PASSED", Branch: "refs/heads/change", CommitSHA: testCommitSHA}

	got, err := s.RecordAttemptCompletion(context.Background(), want)
	if err != nil || got != want {
		t.Fatalf("RecordAttemptCompletion() = %+v, %v; want %+v", got, err, want)
	}
	var validations, refs, events int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM validation_results WHERE id = ?", want.ID).Scan(&validations); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow("SELECT COUNT(*) FROM git_refs WHERE task_id = ? AND attempt_id = ?", task.ID, want.AttemptID).Scan(&refs); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow("SELECT COUNT(*) FROM task_events WHERE task_id = ? AND attempt_id = ? AND event_type = 'attempt.completed'", task.ID, want.AttemptID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if validations != 1 || refs != 1 || events != 1 {
		t.Fatalf("counts = validation %d, refs %d, events %d", validations, refs, events)
	}
	var payload string
	if err := s.db.QueryRow("SELECT payload_json FROM task_events WHERE attempt_id = ? AND event_type = 'attempt.completed'", want.AttemptID).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(payload, "worker output") {
		t.Fatalf("unsafe output in event: %s", payload)
	}
}

func TestRecordAttemptCompletionValidatesStrictlyAndRequiresOwnership(t *testing.T) {
	s := newTestStore(t)
	task := failedTask(t, s, "completion-validation")
	insertAttempt(t, s, "completion-validation-attempt", task.ID, 1, "RUNNING")
	base := CompletionRecord{ID: "completion-validation", TaskID: task.ID, AttemptID: "completion-validation-attempt", Command: "go test", ValidationState: "PASSED", Branch: "main", CommitSHA: testCommitSHA}
	checks := []CompletionRecord{base, base}
	checks[0].ValidationState = "FAILED"
	checks[1].CommitSHA = "not-a-sha"
	for i, record := range checks {
		if _, err := s.RecordAttemptCompletion(context.Background(), record); err == nil {
			t.Fatalf("case %d succeeded", i)
		}
	}
	missing := base
	missing.AttemptID = "missing"
	if _, err := s.RecordAttemptCompletion(context.Background(), missing); !errors.Is(err, ErrAttemptNotFound) {
		t.Fatalf("missing attempt error = %v", err)
	}
	wrongTask := base
	wrongTask.TaskID = "missing-task"
	if _, err := s.RecordAttemptCompletion(context.Background(), wrongTask); !errors.Is(err, ErrAttemptNotFound) {
		t.Fatalf("wrong task error = %v", err)
	}
}

func TestRecordAttemptCompletionReplayIsIdempotentAndConflictsAreExplicit(t *testing.T) {
	s := newTestStore(t)
	task := failedTask(t, s, "completion-replay")
	insertAttempt(t, s, "completion-replay-attempt", task.ID, 1, "RUNNING")
	want := CompletionRecord{ID: "completion-replay", TaskID: task.ID, AttemptID: "completion-replay-attempt", Command: "go test", ValidationState: "PASSED", Branch: "main", CommitSHA: testCommitSHA}
	first, err := s.RecordAttemptCompletion(context.Background(), want)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.RecordAttemptCompletion(context.Background(), want)
	if err != nil || second != first {
		t.Fatalf("replay = %+v, %v", second, err)
	}
	conflict := want
	conflict.Branch = "other"
	if _, err := s.RecordAttemptCompletion(context.Background(), conflict); !errors.Is(err, ErrCompletionConflict) {
		t.Fatalf("conflict error = %v", err)
	}
	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM task_events WHERE attempt_id = ? AND event_type = 'attempt.completed'", want.AttemptID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("completion events = %d, want 1", count)
	}
}
