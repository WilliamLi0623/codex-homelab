package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/WilliamLi0623/codex-homelab/internal/executor/k3s"
	"github.com/WilliamLi0623/codex-homelab/internal/orchestrator"
)

type fakeDispatcher struct{ requests []orchestrator.Request }

func (f *fakeDispatcher) Dispatch(_ context.Context, r orchestrator.Request) (orchestrator.Dispatch, error) {
	f.requests = append(f.requests, r)
	return orchestrator.Dispatch{Claim: orchestrator.Claim{ID: "attempt-1", VMID: 3010}, Job: k3s.Job{ID: "job-1", TaskID: r.TaskID, AttemptID: r.AttemptID, State: k3s.JobRunning}}, nil
}

func TestDispatchTaskOverHTTPUsesPersistedAttempt(t *testing.T) {
	base := newTestServer(t)
	dispatcher := &fakeDispatcher{}
	server := NewServerWithDispatcher(base.store, dispatcher)
	create := httptest.NewRecorder()
	server.ServeHTTP(create, httptest.NewRequest(http.MethodPost, "/v1/tasks", bytes.NewBufferString(`{"repository":"owner/repo","base_ref":"main","objective":"change","idempotency_key":"dispatch-1"}`)))
	var created createTaskResponse
	if err := json.Unmarshal(create.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	start := httptest.NewRecorder()
	server.ServeHTTP(start, httptest.NewRequest(http.MethodPost, "/v1/tasks/"+created.Task.ID+"/attempts", bytes.NewBufferString(`{"profile":"openai-primary"}`)))
	if start.Code != http.StatusCreated {
		t.Fatalf("start status=%d body=%s", start.Code, start.Body.String())
	}
	dispatch := httptest.NewRecorder()
	server.ServeHTTP(dispatch, httptest.NewRequest(http.MethodPost, "/v1/tasks/"+created.Task.ID+"/dispatch", bytes.NewBufferString(`{"attempt_id":"attempt-1","prompt":"run"}`)))
	if dispatch.Code != http.StatusAccepted {
		t.Fatalf("dispatch status=%d body=%s", dispatch.Code, dispatch.Body.String())
	}
}
