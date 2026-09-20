package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/WilliamLi0623/codex-homelab/internal/orchestrator"
	"github.com/WilliamLi0623/codex-homelab/internal/store"
)

type fakeCompleter struct {
	input  orchestrator.CompletionInput
	record store.CompletionRecord
}

func (f *fakeCompleter) Complete(_ context.Context, input orchestrator.CompletionInput) (store.CompletionRecord, error) {
	f.input = input
	return f.record, nil
}

func TestCompleteAttemptOverHTTPUsesExplicitValidationContract(t *testing.T) {
	base := newTestServer(t)
	taskID := createTestTask(t, base)
	started := httptest.NewRecorder()
	base.ServeHTTP(started, httptest.NewRequest(http.MethodPost, "/v1/tasks/"+taskID+"/attempts", bytes.NewBufferString(`{"profile":"openai-primary"}`)))
	var attempt retryTaskResponse
	if started.Code != http.StatusCreated || json.Unmarshal(started.Body.Bytes(), &attempt) != nil {
		t.Fatalf("start status=%d body=%s", started.Code, started.Body.String())
	}
	completer := &fakeCompleter{record: store.CompletionRecord{ID: "completion-1", TaskID: taskID, AttemptID: attempt.Attempt.ID, ValidationState: "PASSED", Branch: "refs/heads/task", CommitSHA: "0123456789abcdef0123456789abcdef01234567"}}
	server := NewServerWithDispatcherAndCompletion(base.store, nil, completer)
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/tasks/"+taskID+"/attempts/"+attempt.Attempt.ID+"/complete", bytes.NewBufferString(`{"branch":"refs/heads/task","validation_command":["go","test","./..."]}`)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("complete status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if completer.input.TaskID != taskID || completer.input.AttemptID != attempt.Attempt.ID || len(completer.input.ValidationCommand) != 3 {
		t.Fatalf("completion input=%+v", completer.input)
	}
}
