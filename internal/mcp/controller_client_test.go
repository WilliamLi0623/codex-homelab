package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/WilliamLi0623/codex-homelab/internal/domain"
	"github.com/WilliamLi0623/codex-homelab/internal/orchestrator"
	"github.com/WilliamLi0623/codex-homelab/internal/store"
)

func TestControllerClientDispatchesThroughExistingController(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/tasks/task-1/dispatch" || request.Header.Get("Authorization") != "Bearer controller-secret" {
			t.Fatalf("request = %s %s auth=%q", request.Method, request.URL.Path, request.Header.Get("Authorization"))
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{"task_id": "task-1", "attempt_id": "attempt-1", "claim_id": "claim-1", "vmid": 3017, "job_id": "job-1", "state": "PENDING"})
	}))
	defer server.Close()
	database, err := store.Open(filepath.Join(t.TempDir(), "controller.sqlite"))
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	defer database.Close()
	client, err := NewControllerClient(server.URL, "controller-secret", database)
	if err != nil {
		t.Fatalf("NewControllerClient() error = %v", err)
	}
	dispatch, err := client.Dispatch(context.Background(), orchestrator.Request{TaskID: "task-1", AttemptID: "attempt-1", Prompt: "run"})
	if err != nil {
		t.Fatalf("Dispatch() error = %v", err)
	}
	if dispatch.Job.ID != "job-1" || dispatch.Claim.VMID != 3017 {
		t.Fatalf("dispatch = %+v, want Controller response", dispatch)
	}
}

func TestControllerClientDeliversContinuationByFindingAttemptOwner(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "controller.sqlite"))
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	defer database.Close()
	task := domain.NewTask("task-1", "owner/repo", "main", "objective", "idempotency-1")
	if _, _, err := database.CreateTask(context.Background(), task); err != nil {
		t.Fatalf("CreateTask() error = %v", err)
	}
	_, attempt, err := database.StartAttempt(context.Background(), task.ID, "openai-primary")
	if err != nil {
		t.Fatalf("StartAttempt() error = %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/tasks/task-1/turns" {
			t.Fatalf("request path = %q", request.URL.Path)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"state":"DELIVERED"}`))
	}))
	defer server.Close()
	client, err := NewControllerClient(server.URL, "", database)
	if err != nil {
		t.Fatalf("NewControllerClient() error = %v", err)
	}
	if err := client.SendMessageForAttempt(context.Background(), attempt.ID, "continue"); err != nil {
		t.Fatalf("SendMessageForAttempt() error = %v", err)
	}
}
