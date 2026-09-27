package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/WilliamLi0623/codex-homelab/internal/api"
	"github.com/WilliamLi0623/codex-homelab/internal/executor/k3s"
	"github.com/WilliamLi0623/codex-homelab/internal/modelrouter"
	"github.com/WilliamLi0623/codex-homelab/internal/orchestrator"
	"github.com/WilliamLi0623/codex-homelab/internal/store"
)

type fakeDispatcher struct{ request orchestrator.Request }

func (f *fakeDispatcher) Dispatch(_ context.Context, r orchestrator.Request) (orchestrator.Dispatch, error) {
	f.request = r
	return orchestrator.Dispatch{Claim: orchestrator.Claim{ID: "claim-1", VMID: 3010}, Job: k3s.Job{ID: "job-1", TaskID: r.TaskID, AttemptID: r.AttemptID, State: k3s.JobRunning}}, nil
}

func TestToolsExposeTheControllerSurface(t *testing.T) {
	server := newTestServer(t)
	tools := server.Tools()
	want := []string{"submit_task", "start_attempt", "get_task", "list_tasks", "send_message", "continue_task", "cancel_task", "retry_task", "get_task_events", "dispatch_task"}
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

func TestDispatchTask(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "controller.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	d := &fakeDispatcher{}
	server := NewServerWithDispatcher(db, d)
	task := submitTestTask(t, server)
	started := call[RetryTaskResult](t, server, "start_attempt", raw(map[string]any{"task_id": task.Task.ID, "profile": "openai-primary"}))
	got := call[DispatchTaskResult](t, server, "dispatch_task", raw(map[string]any{"task_id": task.Task.ID, "attempt_id": started.Attempt.ID, "prompt": "run", "validation_command": []string{"sh", "-c", "printf ok"}}))
	if got.TaskID != task.Task.ID || got.AttemptID != started.Attempt.ID || got.ClaimID != "claim-1" || got.JobID != "job-1" || got.VMID != 3010 || got.State != "RUNNING" {
		t.Fatalf("result=%+v", got)
	}
	if d.request.ModelProfile != "openai-primary" {
		t.Fatalf("model profile=%q", d.request.ModelProfile)
	}
	if d.request.Prompt != "run" {
		t.Fatalf("prompt=%q", d.request.Prompt)
	}
	if len(d.request.ValidationCommand) != 3 || d.request.ValidationCommand[2] != "printf ok" {
		t.Fatalf("validation command=%q", d.request.ValidationCommand)
	}
}

func TestControllerForwardingCreatesFrozenRoutesForStartAndRetry(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "controller.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	controllerDispatcher := &fakeDispatcher{}
	controller := api.NewServerWithRoutingStateAndRoutes(database, controllerDispatcher, nil, nil, nil, "routing-token", controllerTestRoutes(), true)
	controllerServer := httptest.NewServer(controller)
	defer controllerServer.Close()
	controllerClient, err := NewControllerClient(controllerServer.URL, "controller-token", database)
	if err != nil {
		t.Fatal(err)
	}
	server := NewServerWithDispatcherAndMessageSender(database, controllerClient, controllerClient)
	created := submitTestTask(t, server)
	observed := time.Now().UTC().Truncate(time.Millisecond)
	if err := database.SetRoutingState(context.Background(), store.RoutingState{Mode: "normal", ObservedAt: observed, Generation: 4}); err != nil {
		t.Fatal(err)
	}

	started := call[RetryTaskResult](t, server, "start_attempt", raw(map[string]any{"task_id": created.Task.ID, "profile": "openai-primary"}))
	startedRoute, err := database.GetAttemptRoute(context.Background(), created.Task.ID, started.Attempt.ID)
	if err != nil {
		t.Fatalf("start route = %v", err)
	}
	if startedRoute.Mode != "normal" || startedRoute.Generation != 4 || startedRoute.Model != "gpt-6-luna" {
		t.Fatalf("start route = %+v, want Controller-resolved normal generation 4 route", startedRoute)
	}
	call[DispatchTaskResult](t, server, "dispatch_task", raw(map[string]any{"task_id": created.Task.ID, "attempt_id": started.Attempt.ID, "prompt": "run", "validation_command": []string{"go", "test"}}))
	if controllerDispatcher.request.Route == nil || controllerDispatcher.request.Route.Generation != 4 || controllerDispatcher.request.Route.Mode != modelrouter.ModeNormal {
		t.Fatalf("start dispatch route = %+v, want exact frozen start route", controllerDispatcher.request.Route)
	}

	retryTask := submitTestTaskWithKey(t, server, "retry-request-1")
	call[CancelTaskResult](t, server, "cancel_task", raw(map[string]any{"task_id": retryTask.Task.ID}))
	if err := database.SetRoutingState(context.Background(), store.RoutingState{Mode: "quota_fallback", ObservedAt: observed.Add(time.Second), Generation: 5}); err != nil {
		t.Fatal(err)
	}
	retried := call[RetryTaskResult](t, server, "retry_task", raw(map[string]any{"task_id": retryTask.Task.ID}))
	retriedRoute, err := database.GetAttemptRoute(context.Background(), retryTask.Task.ID, retried.Attempt.ID)
	if err != nil {
		t.Fatalf("retry route = %v", err)
	}
	if retriedRoute.Mode != "quota_fallback" || retriedRoute.Generation != 5 || retriedRoute.Model != "glm-5.3-flash" {
		t.Fatalf("retry route = %+v, want Controller-resolved quota generation 5 route", retriedRoute)
	}
	call[DispatchTaskResult](t, server, "dispatch_task", raw(map[string]any{"task_id": retryTask.Task.ID, "attempt_id": retried.Attempt.ID, "prompt": "retry", "validation_command": []string{"go", "test"}}))
	if controllerDispatcher.request.Route == nil || controllerDispatcher.request.Route.Generation != 5 || controllerDispatcher.request.Route.Mode != modelrouter.ModeQuotaFallback {
		t.Fatalf("retry dispatch route = %+v, want exact frozen retry route", controllerDispatcher.request.Route)
	}
}

func controllerTestRoutes() modelrouter.RouteConfig {
	return modelrouter.RouteConfig{
		OpenAI: modelrouter.RouteSettings{Provider: "openai", Model: "gpt-6-luna", WireAPI: modelrouter.WireAPIResponses, ReasoningEffort: "high", BaseURL: "https://api.openai.com/v1", SecretName: "openai-route", SecretKey: "api-key"},
		Spark:  modelrouter.RouteSettings{Provider: "cch", Model: "muse-spark-1.3-contributor", WireAPI: modelrouter.WireAPIResponses, ReasoningEffort: "xhigh", BaseURL: "https://cch-jp.zenkexi.com/v1", SecretName: "cch-route", SecretKey: "api-key"},
		GLM:    modelrouter.RouteSettings{Provider: "cch", Model: "glm-5.3-flash", WireAPI: modelrouter.WireAPIChatCompletions, ReasoningEffort: "max", BaseURL: "https://cch-jp.zenkexi.com/v1", SecretName: "cch-route", SecretKey: "api-key"},
	}
}

func TestDispatchTaskErrors(t *testing.T) {
	server := newTestServer(t)
	task := submitTestTask(t, server)
	_, err := server.CallTool(context.Background(), "dispatch_task", raw(map[string]any{"task_id": task.Task.ID, "attempt_id": "a", "prompt": "p", "validation_command": []string{"sh", "-c", "true"}}))
	if !errors.Is(err, ErrDispatcherUnavailable) {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(t.TempDir(), "controller.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server = NewServerWithDispatcher(db, &fakeDispatcher{})
	_, err = server.CallTool(context.Background(), "dispatch_task", raw(map[string]any{"task_id": "missing", "attempt_id": "a", "prompt": "p", "validation_command": []string{"sh", "-c", "true"}}))
	if !errors.Is(err, store.ErrTaskNotFound) {
		t.Fatal(err)
	}
	_, err = server.CallTool(context.Background(), "dispatch_task", raw(map[string]any{"task_id": "x", "attempt_id": "a"}))
	if !errors.Is(err, ErrInvalidArguments) {
		t.Fatal(err)
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
	return submitTestTaskWithKey(t, server, "request-1")
}

func submitTestTaskWithKey(t *testing.T, server *Server, key string) SubmitTaskResult {
	t.Helper()
	return call[SubmitTaskResult](t, server, "submit_task", raw(map[string]string{"repository": "owner/repository", "base_ref": "main", "objective": "Fix the failing tests", "idempotency_key": key}))
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
