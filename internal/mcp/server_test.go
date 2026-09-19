package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/WilliamLi0623/codex-homelab/internal/store"
)

func TestToolsExposeTheControllerSurface(t *testing.T) {
	server := newTestServer(t)
	tools := server.Tools()
	want := []string{"submit_task", "get_task", "list_tasks", "send_message", "cancel_task", "retry_task", "get_task_events"}
	if len(tools) != len(want) {
		t.Fatalf("tool count = %d, want %d", len(tools), len(want))
	}
	for index, name := range want {
		if tools[index].Name != name {
			t.Fatalf("tool %d name = %q, want %q", index, tools[index].Name, name)
		}
		if tools[index].InputSchema["type"] != "object" {
			t.Fatalf("tool %q input schema type = %v, want object", name, tools[index].InputSchema["type"])
		}
	}
}

func TestSubmitTaskDefaultsToDedicatedLXCAndIsIdempotent(t *testing.T) {
	server := newTestServer(t)
	arguments := json.RawMessage(`{"repository":"owner/repository","base_ref":"main","objective":"Fix the failing tests","idempotency_key":"request-1"}`)

	first := call[SubmitTaskResult](t, server, "submit_task", arguments)
	second := call[SubmitTaskResult](t, server, "submit_task", arguments)
	if !first.Created || second.Created {
		t.Fatalf("created flags = (%t, %t), want (true, false)", first.Created, second.Created)
	}
	if first.Task.ID != second.Task.ID {
		t.Fatalf("task IDs = (%q, %q), want equal", first.Task.ID, second.Task.ID)
	}
	if first.Task.ExecutionClass != "dedicated-lxc" {
		t.Fatalf("execution class = %q, want dedicated-lxc", first.Task.ExecutionClass)
	}
}

func TestGetAndListTasksUsePersistedState(t *testing.T) {
	server := newTestServer(t)
	created := submitTestTask(t, server)

	got := call[GetTaskResult](t, server, "get_task", raw(map[string]any{"task_id": created.Task.ID}))
	listed := call[ListTasksResult](t, server, "list_tasks", json.RawMessage(`{}`))
	if got.Task.ID != created.Task.ID {
		t.Fatalf("get task ID = %q, want %q", got.Task.ID, created.Task.ID)
	}
	if len(listed.Tasks) != 1 || listed.Tasks[0].ID != created.Task.ID {
		t.Fatalf("listed tasks = %+v, want task %q", listed.Tasks, created.Task.ID)
	}
}

func TestSendMessageCreatesObservableEventWithoutExposingBody(t *testing.T) {
	server := newTestServer(t)
	created := submitTestTask(t, server)
	body := "Do not modify the database layer."

	message := call[SendMessageResult](t, server, "send_message", raw(map[string]any{"task_id": created.Task.ID, "body": body}))
	if message.Message.Role != "user" || message.Message.ID == "" {
		t.Fatalf("message = %+v, want persisted user message", message.Message)
	}
	events := call[GetTaskEventsResult](t, server, "get_task_events", raw(map[string]any{"task_id": created.Task.ID}))
	if len(events.Events) != 2 || events.Events[1].Type != "task.message_received" {
		t.Fatalf("events = %+v, want received and message events", events.Events)
	}
	encoded, err := json.Marshal(events)
	if err != nil {
		t.Fatalf("json.Marshal(events) error = %v", err)
	}
	if bytes.Contains(encoded, []byte(body)) {
		t.Fatal("event output exposed message body")
	}
	for _, event := range events.Events {
		if event.Payload != "" {
			t.Fatalf("event payload = %q, want omitted observable metadata only", event.Payload)
		}
	}
}

func TestCancelTaskPersistsCancellation(t *testing.T) {
	server := newTestServer(t)
	created := submitTestTask(t, server)

	cancelled := call[CancelTaskResult](t, server, "cancel_task", raw(map[string]any{"task_id": created.Task.ID}))
	if cancelled.Task.State != "CANCELLED" {
		t.Fatalf("cancelled state = %q, want CANCELLED", cancelled.Task.State)
	}
	got := call[GetTaskResult](t, server, "get_task", raw(map[string]any{"task_id": created.Task.ID}))
	if got.Task.State != "CANCELLED" {
		t.Fatalf("persisted state = %q, want CANCELLED", got.Task.State)
	}
}

func TestRetryCreatesANewAppendOnlyAttempt(t *testing.T) {
	server := newTestServer(t)
	created := submitTestTask(t, server)
	call[CancelTaskResult](t, server, "cancel_task", raw(map[string]any{"task_id": created.Task.ID}))

	retried := call[RetryTaskResult](t, server, "retry_task", raw(map[string]any{"task_id": created.Task.ID}))
	if retried.Attempt.Number != 1 || retried.Attempt.ID == "" {
		t.Fatalf("retry attempt = %+v, want first persisted attempt", retried.Attempt)
	}
	if retried.Task.State != "PLANNED" || retried.Attempt.State != "CREATED" {
		t.Fatalf("retry = %+v, want PLANNED task and CREATED attempt", retried)
	}
	events := call[GetTaskEventsResult](t, server, "get_task_events", raw(map[string]any{"task_id": created.Task.ID}))
	if len(events.Events) != 4 || !hasEventType(events.Events, "attempt.created") {
		t.Fatalf("events = %+v, want append-only attempt.created event", events.Events)
	}
}

func TestCallToolRejectsUnknownFieldsAndUnknownTools(t *testing.T) {
	server := newTestServer(t)
	_, err := server.CallTool(context.Background(), "list_tasks", json.RawMessage(`{"unexpected":true}`))
	if !errors.Is(err, ErrInvalidArguments) {
		t.Fatalf("list_tasks error = %v, want ErrInvalidArguments", err)
	}
	_, err = server.CallTool(context.Background(), "delete_task", json.RawMessage(`{}`))
	if !errors.Is(err, ErrUnknownTool) {
		t.Fatalf("delete_task error = %v, want ErrUnknownTool", err)
	}
}

func submitTestTask(t *testing.T, server *Server) SubmitTaskResult {
	t.Helper()
	return call[SubmitTaskResult](t, server, "submit_task", json.RawMessage(`{"repository":"owner/repository","base_ref":"main","objective":"Fix the failing tests","idempotency_key":"request-1"}`))
}

func call[T any](t *testing.T, server *Server, name string, arguments json.RawMessage) T {
	t.Helper()
	result, err := server.CallTool(context.Background(), name, arguments)
	if err != nil {
		t.Fatalf("CallTool(%q) error = %v", name, err)
	}
	typed, ok := result.(T)
	if !ok {
		t.Fatalf("CallTool(%q) result type = %T", name, result)
	}
	return typed
}

func raw(value any) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return encoded
}

func hasEventType(events []Event, eventType string) bool {
	for _, event := range events {
		if event.Type == eventType {
			return true
		}
	}
	return false
}

func newTestServer(t *testing.T) *Server {
	t.Helper()
	database, err := store.Open(filepath.Join(t.TempDir(), "controller.sqlite"))
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return NewServer(database)
}
