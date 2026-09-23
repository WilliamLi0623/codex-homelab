package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestListAttemptsReturnsAttemptMetadata(t *testing.T) {
	server := newTestServer(t)
	taskID := createTestTask(t, server)
	started := httptest.NewRecorder()
	server.ServeHTTP(started, httptest.NewRequest(http.MethodPost, "/v1/tasks/"+taskID+"/attempts", bytes.NewBufferString(`{"profile":"muse-spark-1.3-contributor"}`)))
	if started.Code != http.StatusCreated {
		t.Fatalf("start attempt status = %d, body = %s", started.Code, started.Body.String())
	}

	listed := httptest.NewRecorder()
	server.ServeHTTP(listed, httptest.NewRequest(http.MethodGet, "/v1/tasks/"+taskID+"/attempts", nil))
	if listed.Code != http.StatusOK {
		t.Fatalf("list attempts status = %d, body = %s", listed.Code, listed.Body.String())
	}
	var response listAttemptsResponse
	if err := json.Unmarshal(listed.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode list attempts response: %v", err)
	}
	if len(response.Attempts) != 1 || response.Attempts[0].ModelProfile != "worker" {
		t.Fatalf("attempts = %+v, want fixed worker role marker", response.Attempts)
	}
}
