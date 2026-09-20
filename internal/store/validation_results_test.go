package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRecordValidationResultPersistsAttemptBoundResultWithoutRawOutputEvent(t *testing.T) {
	database := newTestStore(t)
	task := failedTask(t, database, "task-validation")
	insertAttempt(t, database, "attempt-validation", task.ID, 1, "RUNNING")
	want := ValidationResult{
		ID:        "validation-1",
		AttemptID: "attempt-validation",
		Command:   "go test ./...",
		State:     "PASSED",
		OutputRef: "artifacts/validation-1.log",
		CreatedAt: time.Date(2026, 9, 20, 1, 2, 3, 4, time.UTC),
	}
	const rawOutput = "secret compiler output that must not become an event"

	got, err := database.RecordValidationResult(context.Background(), want, rawOutput)
	if err != nil {
		t.Fatalf("RecordValidationResult() error = %v", err)
	}
	if got != want {
		t.Fatalf("result = %+v, want %+v", got, want)
	}
	stored, err := database.GetValidationResult(context.Background(), want.ID)
	if err != nil {
		t.Fatalf("GetValidationResult() error = %v", err)
	}
	if stored != want {
		t.Fatalf("stored = %+v, want %+v", stored, want)
	}
	events, err := database.ListTaskEvents(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("ListTaskEvents() error = %v", err)
	}
	for _, event := range events {
		if event.Payload == rawOutput || event.Type == "validation.output" {
			t.Fatalf("raw validation output was persisted as event: %+v", event)
		}
	}
}

func TestRecordValidationResultRejectsUnknownAttempt(t *testing.T) {
	database := newTestStore(t)
	_, err := database.RecordValidationResult(context.Background(), ValidationResult{
		ID: "validation-missing-attempt", AttemptID: "does-not-exist", Command: "go test", State: "FAILED",
		CreatedAt: time.Now().UTC(),
	}, "raw output")
	if !errors.Is(err, ErrAttemptNotFound) {
		t.Fatalf("error = %v, want ErrAttemptNotFound", err)
	}
}

func TestRecordValidationResultIsIdempotentAndRejectsConflictingReplay(t *testing.T) {
	database := newTestStore(t)
	task := failedTask(t, database, "task-idempotent-validation")
	insertAttempt(t, database, "attempt-idempotent-validation", task.ID, 1, "RUNNING")
	want := ValidationResult{ID: "validation-replay", AttemptID: "attempt-idempotent-validation", Command: "go test", State: "PASSED", CreatedAt: time.Now().UTC()}
	first, err := database.RecordValidationResult(context.Background(), want, "first raw output")
	if err != nil {
		t.Fatalf("first write error = %v", err)
	}
	second, err := database.RecordValidationResult(context.Background(), want, "different raw output")
	if err != nil {
		t.Fatalf("same replay error = %v", err)
	}
	if second != first {
		t.Fatalf("same replay = %+v, want %+v", second, first)
	}
	conflict := want
	conflict.State = "FAILED"
	if _, err := database.RecordValidationResult(context.Background(), conflict, "raw output"); err == nil {
		t.Fatal("conflicting replay succeeded")
	}
}
