package agentd

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/WilliamLi0623/codex-homelab/internal/domain"
	"github.com/WilliamLi0623/codex-homelab/internal/store"
)

func TestServiceCreatesAndPersistsThreadForAttempt(t *testing.T) {
	database, attemptID := newAttemptStore(t)
	input := strings.NewReader(strings.Join([]string{
		`{"id":1,"result":{}}`,
		`{"id":2,"result":{"thread":{"id":"thr-1"}}}`,
		`{"id":3,"result":{"turn":{"id":"turn-1"}}}`,
		`{"method":"turn/completed","params":{}}`,
	}, "\n") + "\n")
	var output bytes.Buffer
	service := NewService(NewClient(input, &output), database)

	result, err := service.RunAttempt(context.Background(), attemptID, "Fix the tests")
	if err != nil {
		t.Fatalf("RunAttempt() error = %v", err)
	}
	if result.ThreadID != "thr-1" || result.TurnID != "turn-1" {
		t.Fatalf("result = %+v, want persisted thread and started turn", result)
	}
	persisted, err := database.GetCodexThread(context.Background(), attemptID)
	if err != nil || persisted.CodexThreadID != "thr-1" {
		t.Fatalf("GetCodexThread() = (%+v, %v), want thr-1", persisted, err)
	}
}

func TestServiceResumesPersistedThreadForFollowUp(t *testing.T) {
	database, attemptID := newAttemptStore(t)
	if _, _, err := database.RecordCodexThread(context.Background(), attemptID, "thr-1"); err != nil {
		t.Fatalf("RecordCodexThread() error = %v", err)
	}
	input := strings.NewReader(strings.Join([]string{
		`{"id":1,"result":{}}`,
		`{"id":2,"result":{"thread":{"id":"thr-1"}}}`,
		`{"id":3,"result":{"turn":{"id":"turn-2"}}}`,
		`{"method":"turn/completed","params":{}}`,
	}, "\n") + "\n")
	var output bytes.Buffer
	service := NewService(NewClient(input, &output), database)

	if _, err := service.RunAttempt(context.Background(), attemptID, "Do not modify the database layer."); err != nil {
		t.Fatalf("RunAttempt() error = %v", err)
	}
	if !strings.Contains(output.String(), `"method":"thread/resume"`) || strings.Contains(output.String(), `"method":"thread/start"`) {
		t.Fatalf("follow-up did not resume only persisted thread: %s", output.String())
	}
}

func TestServiceInterruptsTurnOnPersistedThread(t *testing.T) {
	database, attemptID := newAttemptStore(t)
	if _, _, err := database.RecordCodexThread(context.Background(), attemptID, "thr-1"); err != nil {
		t.Fatalf("RecordCodexThread() error = %v", err)
	}
	input := strings.NewReader(strings.Join([]string{
		`{"id":1,"result":{}}`,
		`{"id":2,"result":{}}`,
	}, "\n") + "\n")
	var output bytes.Buffer
	service := NewService(NewClient(input, &output), database)

	if err := service.CancelAttempt(context.Background(), attemptID, "turn-1"); err != nil {
		t.Fatalf("CancelAttempt() error = %v", err)
	}
	if !strings.Contains(output.String(), `"method":"turn/interrupt"`) || !strings.Contains(output.String(), `"threadId":"thr-1"`) {
		t.Fatalf("cancellation did not interrupt persisted thread: %s", output.String())
	}
}

func newAttemptStore(t *testing.T) (*store.Store, string) {
	t.Helper()
	database, err := store.Open(filepath.Join(t.TempDir(), "controller.sqlite"))
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	task := domain.NewTask("task-1", "owner/repository", "main", "objective", "idempotency-1")
	if _, _, err := database.CreateTask(context.Background(), task); err != nil {
		t.Fatalf("CreateTask() error = %v", err)
	}
	if _, err := database.CancelTask(context.Background(), task.ID); err != nil {
		t.Fatalf("CancelTask() error = %v", err)
	}
	_, attempt, err := database.RetryTask(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("RetryTask() error = %v", err)
	}
	return database, attempt.ID
}
