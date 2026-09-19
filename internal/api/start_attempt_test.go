package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestStartAttemptOverHTTPPersistsInitialAttempt(t *testing.T) {
	server := newTestServer(t)
	task := createTestTask(t, server)
	payload := []byte(`{"profile":"openai-primary"}`)
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/tasks/"+task+"/attempts", bytes.NewReader(payload)))
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response retryTaskResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Task.ID != task || response.Task.State != "PLANNED" || response.Attempt.Number != 1 {
		t.Fatalf("response=%+v", response)
	}
}
