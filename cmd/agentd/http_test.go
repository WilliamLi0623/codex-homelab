package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/WilliamLi0623/codex-homelab/internal/agentd"
)

func TestHTTPHealthz(t *testing.T) {
	h := newHTTPHandler(newSession(&httpFakeRunner{}))
	r := httptest.NewRecorder()
	h.ServeHTTP(r, httptest.NewRequest(http.MethodGet, "/v1/healthz", nil))
	if r.Code != http.StatusOK || r.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("status=%d content-type=%q", r.Code, r.Header().Get("Content-Type"))
	}
}

func TestHTTPMessagesShareSessionAcrossFollowUp(t *testing.T) {
	runner := &httpFakeRunner{}
	h := newHTTPHandler(newSession(runner))
	post := func(prompt string) response {
		r := httptest.NewRecorder()
		h.ServeHTTP(r, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"prompt":"`+prompt+`"}`)))
		if r.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", r.Code, r.Body.String())
		}
		var got response
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		return got
	}
	first, second := post("first"), post("follow-up")
	if first.ThreadID != second.ThreadID || runner.callsString() != "start,resume,turn,collect" {
		t.Fatalf("thread/calls=%q/%q", first.ThreadID, runner.callsString())
	}
}

func TestHTTPMessagesRejectInvalidRequestsWithoutRunner(t *testing.T) {
	runner := &httpFakeRunner{}
	h := newHTTPHandler(newSession(runner))
	for _, body := range []string{`{}`, `{"prompt":""}`, `{"prompt":"ok","extra":1}`, `{"prompt":"ok"}{}`,
		`{"prompt":"` + strings.Repeat("x", maxRequestLine) + `"}`} {
		r := httptest.NewRecorder()
		h.ServeHTTP(r, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body)))
		if r.Code < 400 || r.Code >= 500 {
			t.Fatalf("body=%q status=%d", body[:min(len(body), 30)], r.Code)
		}
	}
	if len(runner.calls) != 0 {
		t.Fatalf("runner calls=%v", runner.calls)
	}
}

func TestHTTPMessagesRejectConcurrentRequest(t *testing.T) {
	runner := &httpFakeRunner{block: make(chan struct{}), started: make(chan struct{})}
	h := newHTTPHandler(newSession(runner))
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		r := httptest.NewRecorder()
		h.ServeHTTP(r, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"prompt":"first"}`)))
		done <- r
	}()
	<-runner.started
	r := httptest.NewRecorder()
	h.ServeHTTP(r, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"prompt":"second"}`)))
	if r.Code != http.StatusConflict {
		t.Fatalf("status=%d", r.Code)
	}
	close(runner.block)
	if first := <-done; first.Code != http.StatusOK {
		t.Fatalf("first status=%d", first.Code)
	}
}

func TestHTTPMessagesReturnRunnerError(t *testing.T) {
	runner := &httpFakeRunner{err: errors.New("runner failed")}
	h := newHTTPHandler(newSession(runner))
	r := httptest.NewRecorder()
	h.ServeHTTP(r, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"prompt":"x"}`)))
	if r.Code != http.StatusInternalServerError || len(runner.calls) != 1 {
		t.Fatalf("status=%d calls=%v", r.Code, runner.calls)
	}
}

func TestWaitForHTTPServerBlocksUntilContextDone(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		waitForHTTPServer(ctx)
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("HTTP mode returned before context cancellation")
	default:
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("HTTP mode did not stop after context cancellation")
	}
}

type httpFakeRunner struct {
	calls          []string
	err            error
	block, started chan struct{}
}

func (f *httpFakeRunner) StartNewTurn(context.Context, string) (string, []agentd.Event, error) {
	f.calls = append(f.calls, "start")
	if f.started != nil {
		close(f.started)
		f.started = nil
	}
	if f.block != nil {
		<-f.block
	}
	return "thread-1", []agentd.Event{{Method: "turn/completed"}}, f.err
}
func (f *httpFakeRunner) ResumeThread(context.Context, string) error {
	f.calls = append(f.calls, "resume")
	return f.err
}
func (f *httpFakeRunner) StartTurn(context.Context, string, string) (string, error) {
	f.calls = append(f.calls, "turn")
	return "", f.err
}
func (f *httpFakeRunner) CollectTurnEvents(context.Context) ([]agentd.Event, error) {
	f.calls = append(f.calls, "collect")
	return nil, f.err
}
func (f *httpFakeRunner) callsString() string { return strings.Join(f.calls, ",") }
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
