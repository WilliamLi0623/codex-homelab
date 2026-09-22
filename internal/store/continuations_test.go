package store

import (
	"context"
	"errors"
	"testing"

	"github.com/WilliamLi0623/codex-homelab/internal/domain"
)

func TestStartContinuationIsIdempotentAndAtomic(t *testing.T) {
	database := newTestStore(t)
	task := domain.NewTask("task-continuation", "owner/repository", "main", "objective", "request-continuation")
	if _, _, err := database.CreateTask(context.Background(), task); err != nil {
		t.Fatalf("CreateTask() error = %v", err)
	}
	_, attempt, err := database.StartAttempt(context.Background(), task.ID, "openai-primary")
	if err != nil {
		t.Fatalf("StartAttempt() error = %v", err)
	}

	first, created, err := database.StartContinuation(context.Background(), task.ID, attempt.ID, "turn-1", "continue the task")
	if err != nil || !created {
		t.Fatalf("first StartContinuation() = (%+v, %t, %v), want created", first, created, err)
	}
	second, created, err := database.StartContinuation(context.Background(), task.ID, attempt.ID, "turn-1", "continue the task")
	if err != nil || created {
		t.Fatalf("second StartContinuation() = (%+v, %t, %v), want existing record", second, created, err)
	}
	if first.ID != second.ID || first.State != ContinuationPending {
		t.Fatalf("continuations = %+v and %+v, want same pending record", first, second)
	}

	messages, err := database.ListTaskMessages(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("ListTaskMessages() error = %v", err)
	}
	if len(messages) != 1 || messages[0].Body != "continue the task" || messages[0].Role != "user" {
		t.Fatalf("messages = %+v, want one continuation message", messages)
	}
	events, err := database.ListTaskEvents(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("ListTaskEvents() error = %v", err)
	}
	var continuationEvents int
	for _, event := range events {
		if event.Type == "task.continuation_requested" {
			continuationEvents++
		}
	}
	if continuationEvents != 1 {
		t.Fatalf("continuation event count = %d, want 1", continuationEvents)
	}
}

func TestStartContinuationRejectsIdempotencyConflict(t *testing.T) {
	database := newTestStore(t)
	task := domain.NewTask("task-continuation-conflict", "owner/repository", "main", "objective", "request-continuation-conflict")
	if _, _, err := database.CreateTask(context.Background(), task); err != nil {
		t.Fatalf("CreateTask() error = %v", err)
	}
	_, attempt, err := database.StartAttempt(context.Background(), task.ID, "openai-primary")
	if err != nil {
		t.Fatalf("StartAttempt() error = %v", err)
	}
	if _, _, err := database.StartContinuation(context.Background(), task.ID, attempt.ID, "turn-1", "first body"); err != nil {
		t.Fatalf("first StartContinuation() error = %v", err)
	}
	if _, _, err := database.StartContinuation(context.Background(), task.ID, attempt.ID, "turn-1", "different body"); !errors.Is(err, ErrContinuationConflict) {
		t.Fatalf("conflicting StartContinuation() error = %v, want ErrContinuationConflict", err)
	}
}

func TestContinuationStateRoundTrip(t *testing.T) {
	database := newTestStore(t)
	task := domain.NewTask("task-continuation-state", "owner/repository", "main", "objective", "request-continuation-state")
	if _, _, err := database.CreateTask(context.Background(), task); err != nil {
		t.Fatalf("CreateTask() error = %v", err)
	}
	_, attempt, err := database.StartAttempt(context.Background(), task.ID, "openai-primary")
	if err != nil {
		t.Fatalf("StartAttempt() error = %v", err)
	}
	continuation, _, err := database.StartContinuation(context.Background(), task.ID, attempt.ID, "turn-1", "body")
	if err != nil {
		t.Fatalf("StartContinuation() error = %v", err)
	}
	if err := database.UpdateContinuationState(context.Background(), continuation.ID, ContinuationUnknown, "network timeout"); err != nil {
		t.Fatalf("UpdateContinuationState() error = %v", err)
	}
	got, err := database.GetContinuation(context.Background(), continuation.ID)
	if err != nil {
		t.Fatalf("GetContinuation() error = %v", err)
	}
	if got.State != ContinuationUnknown || got.ErrorSummary != "network timeout" {
		t.Fatalf("continuation = %+v, want UNKNOWN with error summary", got)
	}
}
