package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/WilliamLi0623/codex-homelab/internal/domain"
	"github.com/WilliamLi0623/codex-homelab/internal/store"
)

type fakeAttemptMessageSender struct {
	calls []string
	err   error
}

func (f *fakeAttemptMessageSender) SendMessageForAttempt(_ context.Context, attemptID, body string) error {
	f.calls = append(f.calls, attemptID+":"+body)
	return f.err
}

func TestContinueTaskIsIdempotentAndDeliversOnce(t *testing.T) {
	database, taskID, attemptID := continuationFixture(t)
	sender := &fakeAttemptMessageSender{}
	server := NewServerWithDispatcherCompletionReleaseAndMessageSender(database, nil, nil, nil, sender)
	payload := []byte(`{"attempt_id":"` + attemptID + `","body":"continue","idempotency_key":"turn-1"}`)

	first := httptest.NewRecorder()
	server.ServeHTTP(first, httptest.NewRequest(http.MethodPost, "/v1/tasks/"+taskID+"/turns", bytes.NewReader(payload)))
	if first.Code != http.StatusAccepted {
		t.Fatalf("first status = %d, body = %s", first.Code, first.Body.String())
	}
	var firstBody continuationResponse
	if err := json.Unmarshal(first.Body.Bytes(), &firstBody); err != nil {
		t.Fatalf("decode first response: %v", err)
	}
	if firstBody.State != store.ContinuationDelivered {
		t.Fatalf("first state = %q, want DELIVERED", firstBody.State)
	}

	second := httptest.NewRecorder()
	server.ServeHTTP(second, httptest.NewRequest(http.MethodPost, "/v1/tasks/"+taskID+"/turns", bytes.NewReader(payload)))
	if second.Code != http.StatusAccepted {
		t.Fatalf("second status = %d, body = %s", second.Code, second.Body.String())
	}
	var secondBody continuationResponse
	if err := json.Unmarshal(second.Body.Bytes(), &secondBody); err != nil {
		t.Fatalf("decode second response: %v", err)
	}
	if secondBody.ID != firstBody.ID || len(sender.calls) != 1 {
		t.Fatalf("second response=%+v calls=%v, want same continuation and one delivery", secondBody, sender.calls)
	}
}

func TestContinueTaskRejectsIdempotencyConflict(t *testing.T) {
	database, taskID, attemptID := continuationFixture(t)
	sender := &fakeAttemptMessageSender{}
	server := NewServerWithDispatcherCompletionReleaseAndMessageSender(database, nil, nil, nil, sender)
	firstPayload := []byte(`{"attempt_id":"` + attemptID + `","body":"first","idempotency_key":"turn-1"}`)
	first := httptest.NewRecorder()
	server.ServeHTTP(first, httptest.NewRequest(http.MethodPost, "/v1/tasks/"+taskID+"/turns", bytes.NewReader(firstPayload)))
	if first.Code != http.StatusAccepted {
		t.Fatalf("first status = %d, body = %s", first.Code, first.Body.String())
	}
	conflict := httptest.NewRecorder()
	server.ServeHTTP(conflict, httptest.NewRequest(http.MethodPost, "/v1/tasks/"+taskID+"/turns", bytes.NewReader([]byte(`{"attempt_id":"`+attemptID+`","body":"different","idempotency_key":"turn-1"}`))))
	if conflict.Code != http.StatusConflict {
		t.Fatalf("conflict status = %d, body = %s", conflict.Code, conflict.Body.String())
	}
	if len(sender.calls) != 1 {
		t.Fatalf("calls = %v, want one delivery", sender.calls)
	}
}

func TestContinueTaskPersistsUnknownDeliveryWithoutReplay(t *testing.T) {
	database, taskID, attemptID := continuationFixture(t)
	sender := &fakeAttemptMessageSender{err: errors.New("transport timeout")}
	server := NewServerWithDispatcherCompletionReleaseAndMessageSender(database, nil, nil, nil, sender)
	payload := []byte(`{"attempt_id":"` + attemptID + `","body":"continue","idempotency_key":"turn-unknown"}`)

	first := httptest.NewRecorder()
	server.ServeHTTP(first, httptest.NewRequest(http.MethodPost, "/v1/tasks/"+taskID+"/turns", bytes.NewReader(payload)))
	if first.Code != http.StatusAccepted {
		t.Fatalf("first status = %d, body = %s", first.Code, first.Body.String())
	}
	var body continuationResponse
	if err := json.Unmarshal(first.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.State != store.ContinuationUnknown || body.ErrorSummary != "delivery outcome unknown" {
		t.Fatalf("response = %+v, want UNKNOWN delivery", body)
	}

	second := httptest.NewRecorder()
	server.ServeHTTP(second, httptest.NewRequest(http.MethodPost, "/v1/tasks/"+taskID+"/turns", bytes.NewReader(payload)))
	if second.Code != http.StatusAccepted || len(sender.calls) != 1 {
		t.Fatalf("second status=%d calls=%v, want accepted without replay", second.Code, sender.calls)
	}
}

func TestContinueTaskRejectsMissingExecutionHandle(t *testing.T) {
	database, taskID, attemptID := continuationFixture(t)
	sender := &fakeAttemptMessageSender{err: store.ErrExecutionHandleNotFound}
	server := NewServerWithDispatcherCompletionReleaseAndMessageSender(database, nil, nil, nil, sender)
	payload := []byte(`{"attempt_id":"` + attemptID + `","body":"continue","idempotency_key":"turn-rejected"}`)

	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/tasks/"+taskID+"/turns", bytes.NewReader(payload)))
	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var body continuationResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.State != store.ContinuationRejected {
		t.Fatalf("state = %q, want REJECTED", body.State)
	}
}

func TestContinueTaskRequiresSender(t *testing.T) {
	database, taskID, _ := continuationFixture(t)
	server := NewServer(database)
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/tasks/"+taskID+"/turns", bytes.NewBufferString(`{}`)))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func continuationFixture(t *testing.T) (*store.Store, string, string) {
	t.Helper()
	database, err := store.Open(t.TempDir() + "/controller.sqlite")
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	task := domain.NewTask("task-api-continuation", "owner/repository", "main", "objective", "request-api-continuation")
	if _, _, err := database.CreateTask(context.Background(), task); err != nil {
		t.Fatalf("CreateTask() error = %v", err)
	}
	_, attempt, err := database.StartAttempt(context.Background(), task.ID, "openai-primary")
	if err != nil {
		t.Fatalf("StartAttempt() error = %v", err)
	}
	return database, task.ID, attempt.ID
}
