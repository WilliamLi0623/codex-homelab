package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/WilliamLi0623/codex-homelab/internal/orchestrator"
	"github.com/WilliamLi0623/codex-homelab/internal/store"
)

type fakeReleaseReconciler struct {
	claim orchestrator.Claim
	proof string
}

func (f *fakeReleaseReconciler) ReconcileRelease(_ context.Context, claim orchestrator.Claim, proof string) error {
	f.claim = claim
	f.proof = proof
	return nil
}

func TestHealthReportsControllerReady(t *testing.T) {
	server := newTestServer(t)
	recorder := httptest.NewRecorder()

	server.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/health", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
}

func TestReadinessReportsDispatcherUnavailable(t *testing.T) {
	server := newTestServer(t)
	recorder := httptest.NewRecorder()

	server.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/ready", nil))

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusServiceUnavailable)
	}
	if recorder.Body.String() != "{\"error\":\"dispatcher is not configured\"}\n" {
		t.Fatalf("body = %q, want stable readiness error", recorder.Body.String())
	}
}

func TestReadinessReportsConfiguredDispatcher(t *testing.T) {
	base := newTestServer(t)
	server := NewServerWithDispatcher(base.store, &fakeDispatcher{})
	recorder := httptest.NewRecorder()

	server.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/ready", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
}

func TestStatusReportsControllerAndTaskCounts(t *testing.T) {
	server := newTestServer(t)
	createTestTask(t, server)

	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/status", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	var body statusResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode status response: %v", err)
	}
	if body.Controller != "ok" || body.Tasks != 1 {
		t.Fatalf("status response = %+v, want controller ok and one task", body)
	}
}

func TestCreateTaskIsIdempotentOverHTTP(t *testing.T) {
	server := newTestServer(t)
	payload := []byte(`{"repository":"owner/repository","base_ref":"main","objective":"Fix the failing tests","idempotency_key":"request-1"}`)

	first := httptest.NewRecorder()
	server.ServeHTTP(first, httptest.NewRequest(http.MethodPost, "/v1/tasks", bytes.NewReader(payload)))
	if first.Code != http.StatusCreated {
		t.Fatalf("first status = %d, body = %s", first.Code, first.Body.String())
	}
	second := httptest.NewRecorder()
	server.ServeHTTP(second, httptest.NewRequest(http.MethodPost, "/v1/tasks", bytes.NewReader(payload)))
	if second.Code != http.StatusOK {
		t.Fatalf("second status = %d, body = %s", second.Code, second.Body.String())
	}

	var firstBody, secondBody createTaskResponse
	if err := json.Unmarshal(first.Body.Bytes(), &firstBody); err != nil {
		t.Fatalf("decode first response: %v", err)
	}
	if err := json.Unmarshal(second.Body.Bytes(), &secondBody); err != nil {
		t.Fatalf("decode second response: %v", err)
	}
	if firstBody.Task.ID != secondBody.Task.ID {
		t.Fatalf("idempotent IDs differ: %q and %q", firstBody.Task.ID, secondBody.Task.ID)
	}
	if firstBody.Task.ExecutionClass != "dedicated-lxc" {
		t.Fatalf("execution class = %q, want dedicated-lxc", firstBody.Task.ExecutionClass)
	}
}

func TestListAndGetTaskExposePersistedTask(t *testing.T) {
	server := newTestServer(t)
	payload := []byte(`{"repository":"owner/repository","base_ref":"main","objective":"Fix the failing tests","idempotency_key":"request-1"}`)
	created := httptest.NewRecorder()
	server.ServeHTTP(created, httptest.NewRequest(http.MethodPost, "/v1/tasks", bytes.NewReader(payload)))
	if created.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", created.Code, created.Body.String())
	}
	var createBody createTaskResponse
	if err := json.Unmarshal(created.Body.Bytes(), &createBody); err != nil {
		t.Fatalf("decode create response: %v", err)
	}

	listed := httptest.NewRecorder()
	server.ServeHTTP(listed, httptest.NewRequest(http.MethodGet, "/v1/tasks", nil))
	if listed.Code != http.StatusOK {
		t.Fatalf("list status = %d, body = %s", listed.Code, listed.Body.String())
	}
	var listBody listTasksResponse
	if err := json.Unmarshal(listed.Body.Bytes(), &listBody); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if len(listBody.Tasks) != 1 || listBody.Tasks[0].ID != createBody.Task.ID {
		t.Fatalf("list response = %+v, want task %q", listBody, createBody.Task.ID)
	}

	got := httptest.NewRecorder()
	server.ServeHTTP(got, httptest.NewRequest(http.MethodGet, "/v1/tasks/"+createBody.Task.ID, nil))
	if got.Code != http.StatusOK {
		t.Fatalf("get status = %d, body = %s", got.Code, got.Body.String())
	}
	var gotBody createTaskResponse
	if err := json.Unmarshal(got.Body.Bytes(), &gotBody); err != nil {
		t.Fatalf("decode get response: %v", err)
	}
	if gotBody.Task.ID != createBody.Task.ID {
		t.Fatalf("get task ID = %q, want %q", gotBody.Task.ID, createBody.Task.ID)
	}
}

func TestSendMessagePersistsObservableTaskEvent(t *testing.T) {
	server := newTestServer(t)
	taskID := createTestTask(t, server)

	message := httptest.NewRecorder()
	server.ServeHTTP(message, httptest.NewRequest(http.MethodPost, "/v1/tasks/"+taskID+"/messages", bytes.NewBufferString(`{"body":"Do not modify the database layer."}`)))
	if message.Code != http.StatusCreated {
		t.Fatalf("message status = %d, body = %s", message.Code, message.Body.String())
	}
	messages := httptest.NewRecorder()
	server.ServeHTTP(messages, httptest.NewRequest(http.MethodGet, "/v1/tasks/"+taskID+"/messages", nil))
	if messages.Code != http.StatusOK {
		t.Fatalf("messages status = %d, body = %s", messages.Code, messages.Body.String())
	}
	var messageBody listMessagesResponse
	if err := json.Unmarshal(messages.Body.Bytes(), &messageBody); err != nil {
		t.Fatalf("decode messages response: %v", err)
	}
	if len(messageBody.Messages) != 1 || messageBody.Messages[0].Body != "Do not modify the database layer." {
		t.Fatalf("messages = %+v, want persisted user message", messageBody)
	}

	events := httptest.NewRecorder()
	server.ServeHTTP(events, httptest.NewRequest(http.MethodGet, "/v1/tasks/"+taskID+"/events", nil))
	if events.Code != http.StatusOK {
		t.Fatalf("events status = %d, body = %s", events.Code, events.Body.String())
	}
	var body listEventsResponse
	if err := json.Unmarshal(events.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode events response: %v", err)
	}
	if len(body.Events) != 2 {
		t.Fatalf("event count = %d, want 2", len(body.Events))
	}
	if body.Events[1].Type != "task.message_received" {
		t.Fatalf("second event type = %q, want task.message_received", body.Events[1].Type)
	}
}

func TestCancelTaskPersistsCancellation(t *testing.T) {
	server := newTestServer(t)
	taskID := createTestTask(t, server)

	cancelled := httptest.NewRecorder()
	server.ServeHTTP(cancelled, httptest.NewRequest(http.MethodPost, "/v1/tasks/"+taskID+"/cancel", nil))
	if cancelled.Code != http.StatusOK {
		t.Fatalf("cancel status = %d, body = %s", cancelled.Code, cancelled.Body.String())
	}
	var body createTaskResponse
	if err := json.Unmarshal(cancelled.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode cancel response: %v", err)
	}
	if body.Task.State != "CANCELLED" {
		t.Fatalf("cancelled state = %q, want CANCELLED", body.Task.State)
	}
}

func TestRetryCreatesNewAttemptForCancelledTask(t *testing.T) {
	server := newTestServer(t)
	taskID := createTestTask(t, server)
	cancelled := httptest.NewRecorder()
	server.ServeHTTP(cancelled, httptest.NewRequest(http.MethodPost, "/v1/tasks/"+taskID+"/cancel", nil))
	if cancelled.Code != http.StatusOK {
		t.Fatalf("cancel status = %d, body = %s", cancelled.Code, cancelled.Body.String())
	}

	retried := httptest.NewRecorder()
	server.ServeHTTP(retried, httptest.NewRequest(http.MethodPost, "/v1/tasks/"+taskID+"/retry", nil))
	if retried.Code != http.StatusCreated {
		t.Fatalf("retry status = %d, body = %s", retried.Code, retried.Body.String())
	}
	var body retryTaskResponse
	if err := json.Unmarshal(retried.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode retry response: %v", err)
	}
	if body.Task.State != "PLANNED" {
		t.Fatalf("retry task state = %q, want PLANNED", body.Task.State)
	}
	if body.Attempt.Number != 1 || body.Attempt.State != "CREATED" {
		t.Fatalf("retry attempt = %+v, want first CREATED attempt", body.Attempt)
	}
}

func TestReconcileAttemptRejectsUnknownOutcome(t *testing.T) {
	server := newTestServer(t)
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/tasks/task-1/attempts/attempt-1/reconcile", bytes.NewBufferString(`{"outcome":"UNKNOWN"}`)))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestReconcileReleaseUsesDurableClaimIdentity(t *testing.T) {
	base := newTestServer(t)
	taskID := createTestTask(t, base)
	claim, _, err := base.store.ClaimCapacity(context.Background(), store.CapacityClaimRequest{
		TaskID: taskID, AttemptID: "attempt-release", Generation: "gen-release", Priority: 1, VMID: 3010,
	})
	if err != nil {
		t.Fatalf("claim capacity: %v", err)
	}
	if _, _, err := base.store.EnsureReleaseProgress(context.Background(), store.ReleaseProgressRequest{TaskID: taskID, AttemptID: "attempt-release", VMID: 3010, Generation: "gen-release", KubeNode: "codex-3010"}); err != nil {
		t.Fatalf("ensure release progress: %v", err)
	}
	if err := base.store.UpdateReleaseProgress(context.Background(), taskID, "attempt-release", store.ReleaseStepCordon, store.ReleaseStateUnknown, "external outcome unknown"); err != nil {
		t.Fatalf("mark release unknown: %v", err)
	}
	reconciler := &fakeReleaseReconciler{}
	server := NewServerWithDispatcherCompletionAndRelease(base.store, nil, nil, reconciler)
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost,
		"/v1/tasks/"+taskID+"/attempts/attempt-release/release/reconcile",
		bytes.NewBufferString(`{"vmid":3010,"generation":"gen-release","kube_node":"codex-3010","proof":"observed node and guest identity"}`)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if reconciler.claim.ID != claim.ID || reconciler.claim.VMID != 3010 || reconciler.claim.Generation != "gen-release" || reconciler.claim.KubeNode != "codex-3010" {
		t.Fatalf("reconciled claim = %+v, want durable identity", reconciler.claim)
	}
	if reconciler.proof != "observed node and guest identity" {
		t.Fatalf("proof = %q", reconciler.proof)
	}
}

func TestReconcileReleaseRejectsClaimIdentityMismatch(t *testing.T) {
	base := newTestServer(t)
	taskID := createTestTask(t, base)
	if _, _, err := base.store.ClaimCapacity(context.Background(), store.CapacityClaimRequest{
		TaskID: taskID, AttemptID: "attempt-release", Generation: "gen-release", Priority: 1, VMID: 3010,
	}); err != nil {
		t.Fatalf("claim capacity: %v", err)
	}
	reconciler := &fakeReleaseReconciler{}
	server := NewServerWithDispatcherCompletionAndRelease(base.store, nil, nil, reconciler)
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost,
		"/v1/tasks/"+taskID+"/attempts/attempt-release/release/reconcile",
		bytes.NewBufferString(`{"vmid":3011,"generation":"gen-release","kube_node":"codex-3010","proof":"observed node and guest identity"}`)))
	if recorder.Code != http.StatusConflict || reconciler.claim.ID != "" {
		t.Fatalf("status = %d, body = %s, reconciler = %+v; want conflict without call", recorder.Code, recorder.Body.String(), reconciler)
	}
}

func TestReconcileReleaseRequiresProof(t *testing.T) {
	base := newTestServer(t)
	server := NewServerWithDispatcherCompletionAndRelease(base.store, nil, nil, &fakeReleaseReconciler{})
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost,
		"/v1/tasks/task-1/attempts/attempt-release/release/reconcile",
		bytes.NewBufferString(`{"vmid":3010,"generation":"gen-release","kube_node":"codex-3010","proof":" "}`)))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s; want bad request", recorder.Code, recorder.Body.String())
	}
}

func TestReconcileReleaseRequiresUnknownDurableProgress(t *testing.T) {
	base := newTestServer(t)
	taskID := createTestTask(t, base)
	if _, _, err := base.store.ClaimCapacity(context.Background(), store.CapacityClaimRequest{
		TaskID: taskID, AttemptID: "attempt-release", Generation: "gen-release", Priority: 1, VMID: 3010,
	}); err != nil {
		t.Fatalf("claim capacity: %v", err)
	}
	if _, _, err := base.store.EnsureReleaseProgress(context.Background(), store.ReleaseProgressRequest{TaskID: taskID, AttemptID: "attempt-release", VMID: 3010, Generation: "gen-release", KubeNode: "codex-3010"}); err != nil {
		t.Fatalf("ensure release progress: %v", err)
	}
	reconciler := &fakeReleaseReconciler{}
	server := NewServerWithDispatcherCompletionAndRelease(base.store, nil, nil, reconciler)
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost,
		"/v1/tasks/"+taskID+"/attempts/attempt-release/release/reconcile",
		bytes.NewBufferString(`{"vmid":3010,"generation":"gen-release","kube_node":"codex-3010","proof":"observed"}`)))
	if recorder.Code != http.StatusConflict || reconciler.claim.ID != "" {
		t.Fatalf("status = %d, body = %s, reconciler = %+v; want conflict without call", recorder.Code, recorder.Body.String(), reconciler)
	}
}

func TestDispatchRejectsUnknownAttemptBeforeDispatcher(t *testing.T) {
	base := newTestServer(t)
	dispatcher := &fakeDispatcher{}
	server := NewServerWithDispatcher(base.store, dispatcher)
	taskID := createTestTask(t, server)
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/tasks/"+taskID+"/dispatch", bytes.NewBufferString(`{"attempt_id":"missing","prompt":"run"}`)))
	if recorder.Code < http.StatusBadRequest || recorder.Code >= http.StatusInternalServerError || len(dispatcher.requests) != 0 {
		t.Fatalf("status = %d, requests = %d, body = %s; want 4xx and no dispatch", recorder.Code, len(dispatcher.requests), recorder.Body.String())
	}
}

func TestDispatchRejectsMissingValidationCommandBeforeDispatcher(t *testing.T) {
	base := newTestServer(t)
	dispatcher := &fakeDispatcher{}
	server := NewServerWithDispatcher(base.store, dispatcher)
	taskID := createTestTask(t, server)
	_, started, err := base.store.StartAttempt(context.Background(), taskID, "attempt-validation-required")
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/tasks/"+taskID+"/dispatch", bytes.NewBufferString(`{"attempt_id":"`+started.ID+`","prompt":"run"}`)))
	if recorder.Code != http.StatusBadRequest || len(dispatcher.requests) != 0 {
		t.Fatalf("status = %d, requests = %d, body = %s; want 400 and no dispatch", recorder.Code, len(dispatcher.requests), recorder.Body.String())
	}
}

func TestDispatchRejectsAttemptBelongingToAnotherTaskBeforeDispatcher(t *testing.T) {
	base := newTestServer(t)
	dispatcher := &fakeDispatcher{}
	server := NewServerWithDispatcher(base.store, dispatcher)
	firstTaskID := createTestTask(t, server)
	secondTaskID := createSecondTestTask(t, server)
	start := httptest.NewRecorder()
	server.ServeHTTP(start, httptest.NewRequest(http.MethodPost, "/v1/tasks/"+firstTaskID+"/attempts", bytes.NewBufferString(`{"profile":"openai-primary"}`)))
	var started retryTaskResponse
	if start.Code != http.StatusCreated || json.Unmarshal(start.Body.Bytes(), &started) != nil {
		t.Fatalf("start status = %d, body = %s", start.Code, start.Body.String())
	}
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/tasks/"+secondTaskID+"/dispatch", bytes.NewBufferString(`{"attempt_id":"`+started.Attempt.ID+`","prompt":"run"}`)))
	if recorder.Code < http.StatusBadRequest || recorder.Code >= http.StatusInternalServerError || len(dispatcher.requests) != 0 {
		t.Fatalf("status = %d, requests = %d, body = %s; want 4xx and no dispatch", recorder.Code, len(dispatcher.requests), recorder.Body.String())
	}
}

func createSecondTestTask(t *testing.T, server *Server) string {
	t.Helper()
	created := httptest.NewRecorder()
	server.ServeHTTP(created, httptest.NewRequest(http.MethodPost, "/v1/tasks", bytes.NewBufferString(`{"repository":"owner/repository","base_ref":"main","objective":"Another change","idempotency_key":"request-2"}`)))
	var body createTaskResponse
	if created.Code != http.StatusCreated || json.Unmarshal(created.Body.Bytes(), &body) != nil {
		t.Fatalf("create status = %d, body = %s", created.Code, created.Body.String())
	}
	return body.Task.ID
}

func createTestTask(t *testing.T, server *Server) string {
	t.Helper()
	payload := []byte(`{"repository":"owner/repository","base_ref":"main","objective":"Fix the failing tests","idempotency_key":"request-1"}`)
	created := httptest.NewRecorder()
	server.ServeHTTP(created, httptest.NewRequest(http.MethodPost, "/v1/tasks", bytes.NewReader(payload)))
	if created.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", created.Code, created.Body.String())
	}
	var body createTaskResponse
	if err := json.Unmarshal(created.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	return body.Task.ID
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
