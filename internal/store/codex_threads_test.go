package store

import (
	"context"
	"errors"
	"testing"

	"github.com/WilliamLi0623/codex-homelab/internal/domain"
)

func TestRecordCodexThreadPersistsAttemptMappingIdempotently(t *testing.T) {
	database := newTestStore(t)
	task := failedTask(t, database, "task-1")
	insertAttempt(t, database, "attempt-1", task.ID, 1, domain.AttemptCreated)

	first, created, err := database.RecordCodexThread(context.Background(), "attempt-1", "thr-1")
	if err != nil || !created {
		t.Fatalf("RecordCodexThread() = (%+v, %t, %v), want created mapping", first, created, err)
	}
	second, created, err := database.RecordCodexThread(context.Background(), "attempt-1", "thr-1")
	if err != nil || created || second.ID != first.ID {
		t.Fatalf("second RecordCodexThread() = (%+v, %t, %v), want existing mapping", second, created, err)
	}
	loaded, err := database.GetCodexThread(context.Background(), "attempt-1")
	if err != nil || loaded.CodexThreadID != "thr-1" {
		t.Fatalf("GetCodexThread() = (%+v, %v), want persisted thr-1", loaded, err)
	}
}

func TestRecordCodexThreadRejectsChangedThreadForAttempt(t *testing.T) {
	database := newTestStore(t)
	task := failedTask(t, database, "task-1")
	insertAttempt(t, database, "attempt-1", task.ID, 1, domain.AttemptCreated)
	if _, _, err := database.RecordCodexThread(context.Background(), "attempt-1", "thr-1"); err != nil {
		t.Fatalf("initial RecordCodexThread() error = %v", err)
	}
	if _, _, err := database.RecordCodexThread(context.Background(), "attempt-1", "thr-2"); !errors.Is(err, ErrCodexThreadConflict) {
		t.Fatalf("changed RecordCodexThread() error = %v, want ErrCodexThreadConflict", err)
	}
}
