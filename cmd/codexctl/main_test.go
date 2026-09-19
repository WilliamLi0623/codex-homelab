package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDoctorReportsHealthyController(t *testing.T) {
	server := newTestServer(t, func(writer http.ResponseWriter, request *http.Request) {
		assertRequest(t, request, http.MethodGet, "/v1/health")
		writeTestJSON(t, writer, http.StatusOK, map[string]string{"status": "ok"})
	})
	if got, want := runCommand(t, server, "doctor"), "controller: ok\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestRunCreatesTaskWithRequiredIdempotencyKey(t *testing.T) {
	server := newTestServer(t, func(writer http.ResponseWriter, request *http.Request) {
		assertRequest(t, request, http.MethodPost, "/v1/tasks")
		var got map[string]string
		if err := json.NewDecoder(request.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		want := map[string]string{"repository": "owner/repo", "base_ref": "main", "objective": "Fix tests", "profile": "economy", "idempotency_key": "request-1"}
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("request = %v, want %v", got, want)
		}
		writeTestJSON(t, writer, http.StatusCreated, taskEnvelope(testTask("task-1", "RECEIVED")))
	})
	got := runCommand(t, server, "run", "--repository", "owner/repo", "--base-ref", "main", "--objective", "Fix tests", "--profile", "economy", "--idempotency-key", "request-1")
	if want := taskOutput(testTask("task-1", "RECEIVED")); got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestRunRequiresIdempotencyKeyBeforeRequest(t *testing.T) {
	server := newTestServer(t, func(http.ResponseWriter, *http.Request) { t.Fatal("unexpected HTTP request") })
	var output bytes.Buffer
	err := run(withEndpoint(server, "run", "--repository", "owner/repo", "--objective", "Fix tests"), &output, server.Client())
	if err == nil || !strings.Contains(err.Error(), "--idempotency-key is required") {
		t.Fatalf("error = %v, want required idempotency key", err)
	}
}

func TestListPrintsStableTaskTable(t *testing.T) {
	server := newTestServer(t, func(writer http.ResponseWriter, request *http.Request) {
		assertRequest(t, request, http.MethodGet, "/v1/tasks")
		writeTestJSON(t, writer, http.StatusOK, map[string]any{"tasks": []any{testTask("task-1", "RUNNING"), testTask("task-2", "RECEIVED")}})
	})
	want := "ID\tSTATE\tEXECUTION CLASS\tREPOSITORY\tBASE REF\n" +
		"task-1\tRUNNING\tdedicated-lxc\towner/repo\tmain\n" +
		"task-2\tRECEIVED\tdedicated-lxc\towner/repo\tmain\n"
	if got := runCommand(t, server, "list"); got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestShowPrintsTask(t *testing.T) {
	server := newTestServer(t, func(writer http.ResponseWriter, request *http.Request) {
		assertRequest(t, request, http.MethodGet, "/v1/tasks/task-1")
		writeTestJSON(t, writer, http.StatusOK, taskEnvelope(testTask("task-1", "RUNNING")))
	})
	if got, want := runCommand(t, server, "show", "task-1"), taskOutput(testTask("task-1", "RUNNING")); got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestSendPostsMessage(t *testing.T) {
	server := newTestServer(t, func(writer http.ResponseWriter, request *http.Request) {
		assertRequest(t, request, http.MethodPost, "/v1/tasks/task-1/messages")
		var got map[string]string
		if err := json.NewDecoder(request.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if got["body"] != "Check Windows" {
			t.Fatalf("body = %q", got["body"])
		}
		writeTestJSON(t, writer, http.StatusCreated, map[string]string{"id": "message-1", "role": "user"})
	})
	if got, want := runCommand(t, server, "send", "task-1", "--body", "Check Windows"), "message: message-1\nrole: user\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestCancelPrintsUpdatedTask(t *testing.T) {
	server := taskActionServer(t, "/v1/tasks/task-1/cancel", http.StatusOK, taskEnvelope(testTask("task-1", "CANCELLED")))
	if got, want := runCommand(t, server, "cancel", "task-1"), taskOutput(testTask("task-1", "CANCELLED")); got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestRetryPrintsTaskAndAttempt(t *testing.T) {
	server := taskActionServer(t, "/v1/tasks/task-1/retry", http.StatusCreated, map[string]any{
		"task": testTask("task-1", "PLANNED"), "attempt": map[string]any{"id": "attempt-1", "number": 1, "model_profile": "standard", "state": "CREATED"},
	})
	want := taskOutput(testTask("task-1", "PLANNED")) + "attempt: attempt-1\nattempt number: 1\nmodel profile: standard\nattempt state: CREATED\n"
	if got := runCommand(t, server, "retry", "task-1"); got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestLogsPrintStableEventTable(t *testing.T) {
	server := newTestServer(t, func(writer http.ResponseWriter, request *http.Request) {
		assertRequest(t, request, http.MethodGet, "/v1/tasks/task-1/events")
		writeTestJSON(t, writer, http.StatusOK, map[string]any{"events": []any{
			map[string]string{"id": "event-1", "type": "task.received", "created_at": "2026-09-19T01:02:03Z"},
			map[string]string{"id": "event-2", "type": "task.message_received", "created_at": "2026-09-19T01:03:04Z"},
		}})
	})
	want := "CREATED AT\tTYPE\tID\n2026-09-19T01:02:03Z\ttask.received\tevent-1\n2026-09-19T01:03:04Z\ttask.message_received\tevent-2\n"
	if got := runCommand(t, server, "logs", "task-1"); got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestStatusPrintsControllerSummary(t *testing.T) {
	server := newTestServer(t, func(writer http.ResponseWriter, request *http.Request) {
		assertRequest(t, request, http.MethodGet, "/v1/status")
		writeTestJSON(t, writer, http.StatusOK, map[string]any{"controller": "ok", "tasks": 2})
	})
	if got, want := runCommand(t, server, "status"), "controller: ok\ntasks: 2\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestReconcilePostsVerifiedOutcome(t *testing.T) {
	server := newTestServer(t, func(writer http.ResponseWriter, request *http.Request) {
		assertRequest(t, request, http.MethodPost, "/v1/tasks/task-1/attempts/attempt-1/reconcile")
		var got map[string]string
		if err := json.NewDecoder(request.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if got["outcome"] != "EXECUTION_FAILED" {
			t.Fatalf("outcome = %q", got["outcome"])
		}
		writeTestJSON(t, writer, http.StatusOK, map[string]any{"id": "attempt-1", "number": 1, "model_profile": "standard", "state": "EXECUTION_FAILED"})
	})
	got := runCommand(t, server, "reconcile", "task-1", "attempt-1", "--outcome", "EXECUTION_FAILED")
	want := "attempt: attempt-1\nattempt number: 1\nmodel profile: standard\nattempt state: EXECUTION_FAILED\n"
	if got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestReconcileRequiresOutcomeBeforeRequest(t *testing.T) {
	server := newTestServer(t, func(http.ResponseWriter, *http.Request) { t.Fatal("unexpected HTTP request") })
	var output bytes.Buffer
	err := run(withEndpoint(server, "reconcile", "task-1", "attempt-1"), &output, server.Client())
	if err == nil || err.Error() != "--outcome is required" {
		t.Fatalf("error = %v", err)
	}
}

func TestControllerErrorIncludesStatusAndMessage(t *testing.T) {
	server := newTestServer(t, func(writer http.ResponseWriter, request *http.Request) {
		writeTestJSON(t, writer, http.StatusConflict, map[string]string{"error": "task cannot be retried"})
	})
	var output bytes.Buffer
	err := run(withEndpoint(server, "retry", "task-1"), &output, server.Client())
	if err == nil || err.Error() != "controller returned 409 Conflict: task cannot be retried" {
		t.Fatalf("error = %v", err)
	}
}

func TestPendingCommandsAreExplicitlyUnsupported(t *testing.T) {
	for _, command := range []string{"nodes", "capacity", "providers"} {
		t.Run(command, func(t *testing.T) {
			var output bytes.Buffer
			err := run([]string{command}, &output, http.DefaultClient)
			want := fmt.Sprintf("command %q is unsupported: Controller endpoint is not implemented", command)
			if err == nil || err.Error() != want {
				t.Fatalf("error = %v, want %q", err, want)
			}
		})
	}
}

func TestUnknownOptionFailsBeforeRequest(t *testing.T) {
	server := newTestServer(t, func(http.ResponseWriter, *http.Request) { t.Fatal("unexpected HTTP request") })
	var output bytes.Buffer
	err := run(withEndpoint(server, "show", "task-1", "--repository", "owner/repo"), &output, server.Client())
	if err == nil || err.Error() != "show: unknown option --repository" {
		t.Fatalf("error = %v", err)
	}
}

func TestExtraTaskIDFailsBeforeRequest(t *testing.T) {
	server := newTestServer(t, func(http.ResponseWriter, *http.Request) { t.Fatal("unexpected HTTP request") })
	var output bytes.Buffer
	err := run(withEndpoint(server, "cancel", "task-1", "task-2"), &output, server.Client())
	if err == nil || err.Error() != "cancel requires exactly one task ID" {
		t.Fatalf("error = %v", err)
	}
}

func newTestServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server
}
func taskActionServer(t *testing.T, path string, status int, body any) *httptest.Server {
	t.Helper()
	return newTestServer(t, func(writer http.ResponseWriter, request *http.Request) {
		assertRequest(t, request, http.MethodPost, path)
		writeTestJSON(t, writer, status, body)
	})
}
func runCommand(t *testing.T, server *httptest.Server, arguments ...string) string {
	t.Helper()
	var output bytes.Buffer
	if err := run(withEndpoint(server, arguments...), &output, server.Client()); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	return output.String()
}
func withEndpoint(server *httptest.Server, arguments ...string) []string {
	return append(arguments, "--endpoint", server.URL)
}
func assertRequest(t *testing.T, request *http.Request, method, path string) {
	t.Helper()
	if request.Method != method || request.URL.Path != path {
		t.Fatalf("request = %s %s, want %s %s", request.Method, request.URL.Path, method, path)
	}
}
func writeTestJSON(t *testing.T, writer http.ResponseWriter, status int, body any) {
	t.Helper()
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	if err := json.NewEncoder(writer).Encode(body); err != nil {
		t.Fatalf("encode response: %v", err)
	}
}
func testTask(id, state string) map[string]string {
	return map[string]string{"id": id, "repository": "owner/repo", "base_ref": "main", "objective": "Fix tests", "execution_class": "dedicated-lxc", "state": state}
}
func taskEnvelope(task map[string]string) map[string]any { return map[string]any{"task": task} }
func taskOutput(task map[string]string) string {
	return fmt.Sprintf("id: %s\nstate: %s\nexecution class: %s\nrepository: %s\nbase ref: %s\nobjective: %s\n", task["id"], task["state"], task["execution_class"], task["repository"], task["base_ref"], task["objective"])
}
