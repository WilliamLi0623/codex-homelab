package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/WilliamLi0623/codex-homelab/internal/store"
)

func TestHealthReportsControllerReady(t *testing.T) {
	server := newTestServer(t)
	recorder := httptest.NewRecorder()

	server.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/health", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
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

func newTestServer(t *testing.T) *Server {
	t.Helper()
	database, err := store.Open(filepath.Join(t.TempDir(), "controller.sqlite"))
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return NewServer(database)
}
