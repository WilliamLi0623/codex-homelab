package store

import (
	"context"
	"testing"

	"github.com/WilliamLi0623/codex-homelab/internal/domain"
)

func TestExecutionHandleIsIdempotentAndDurable(t *testing.T) {
	database := newTestStore(t)
	task := failedTask(t, database, "task-handle")
	insertAttempt(t, database, "attempt-handle", task.ID, 1, domain.AttemptCreated)
	first, created, err := database.RecordExecutionHandle(context.Background(), "attempt-handle", "k3s", "job-1", "RUNNING")
	if err != nil || !created {
		t.Fatalf("first record = (%+v, %t, %v)", first, created, err)
	}
	second, created, err := database.RecordExecutionHandle(context.Background(), "attempt-handle", "k3s", "job-1", "RUNNING")
	if err != nil || created || second.ID != first.ID {
		t.Fatalf("second record = (%+v, %t, %v)", second, created, err)
	}
	if err := database.UpdateExecutionHandleState(context.Background(), "job-1", "SUCCEEDED"); err != nil {
		t.Fatal(err)
	}
	loaded, err := database.GetExecutionHandleByExternalID(context.Background(), "job-1")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.State != "SUCCEEDED" || loaded.AttemptID != "attempt-handle" {
		t.Fatalf("loaded = %+v", loaded)
	}
}
