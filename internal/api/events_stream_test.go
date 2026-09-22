package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestListEventsSupportsAfterCursor(t *testing.T) {
	server := newTestServer(t)
	taskID := createTestTask(t, server)
	first := httptest.NewRecorder()
	server.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/v1/tasks/"+taskID+"/events", nil))
	if first.Code != http.StatusOK {
		t.Fatalf("first events status = %d, body = %s", first.Code, first.Body.String())
	}
	var initial listEventsResponse
	if err := json.Unmarshal(first.Body.Bytes(), &initial); err != nil {
		t.Fatalf("decode initial events: %v", err)
	}
	if len(initial.Events) != 1 {
		t.Fatalf("initial events = %+v, want one event", initial.Events)
	}

	cancel := httptest.NewRecorder()
	server.ServeHTTP(cancel, httptest.NewRequest(http.MethodPost, "/v1/tasks/"+taskID+"/cancel", nil))
	if cancel.Code != http.StatusOK {
		t.Fatalf("cancel status = %d, body = %s", cancel.Code, cancel.Body.String())
	}

	after := httptest.NewRecorder()
	server.ServeHTTP(after, httptest.NewRequest(http.MethodGet, "/v1/tasks/"+taskID+"/events?after="+initial.Events[0].ID, nil))
	if after.Code != http.StatusOK {
		t.Fatalf("after status = %d, body = %s", after.Code, after.Body.String())
	}
	var response listEventsResponse
	if err := json.Unmarshal(after.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode after events: %v", err)
	}
	if len(response.Events) != 1 || response.Events[0].Type != "task.cancelled" {
		t.Fatalf("after events = %+v, want task.cancelled", response.Events)
	}
}

func TestListEventsRejectsUnknownCursor(t *testing.T) {
	server := newTestServer(t)
	taskID := createTestTask(t, server)
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/tasks/"+taskID+"/events?after=missing-event", nil))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s; want 400", recorder.Code, recorder.Body.String())
	}
}

func TestEventStreamEmitsSSEAndClosesOnTerminalEvent(t *testing.T) {
	server := newTestServer(t)
	taskID := createTestTask(t, server)
	cancel := httptest.NewRecorder()
	server.ServeHTTP(cancel, httptest.NewRequest(http.MethodPost, "/v1/tasks/"+taskID+"/cancel", nil))
	if cancel.Code != http.StatusOK {
		t.Fatalf("cancel status = %d, body = %s", cancel.Code, cancel.Body.String())
	}

	stream := httptest.NewRecorder()
	server.ServeHTTP(stream, httptest.NewRequest(http.MethodGet, "/v1/tasks/"+taskID+"/events/stream", nil))
	if stream.Code != http.StatusOK {
		t.Fatalf("stream status = %d, body = %s", stream.Code, stream.Body.String())
	}
	if got := stream.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("content type = %q, want text/event-stream", got)
	}
	body := stream.Body.Bytes()
	for _, want := range [][]byte{
		[]byte("event: task.received\n"),
		[]byte("event: task.cancelled\n"),
		[]byte("\n\n"),
	} {
		if !bytes.Contains(body, want) {
			t.Fatalf("stream body %q does not contain %q", body, want)
		}
	}
}
