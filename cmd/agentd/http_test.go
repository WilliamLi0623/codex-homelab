package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

func TestHTTPResultInitiallyNotFound(t *testing.T) {
	h := newHTTPHandler(newSession(&httpFakeRunner{}))
	r := httptest.NewRecorder()
	h.ServeHTTP(r, httptest.NewRequest(http.MethodGet, "/v1/result", nil))
	if r.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", r.Code, r.Body.String())
	}
}

func TestHTTPResultReturnsMostRecentSuccessfulResponse(t *testing.T) {
	h := newHTTPHandler(newSession(&httpFakeRunner{}))
	post := httptest.NewRecorder()
	h.ServeHTTP(post, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"prompt":"first"}`)))
	if post.Code != http.StatusOK {
		t.Fatalf("post status=%d body=%s", post.Code, post.Body.String())
	}
	get := httptest.NewRecorder()
	h.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/v1/result", nil))
	if get.Code != http.StatusOK {
		t.Fatalf("get status=%d body=%s", get.Code, get.Body.String())
	}
	var got response
	if err := json.NewDecoder(get.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.ThreadID != "thread-1" || len(got.Events) != 1 || got.Events[0] != "turn/completed" {
		t.Fatalf("result=%+v", got)
	}
}

func TestInitialEnvironmentRequestUsesStrictJSON(t *testing.T) {
	valid := `{"prompt":"initial"}`
	input, err := decodeEnvironmentRequest(valid)
	if err != nil || input.Prompt != "initial" {
		t.Fatalf("input=%+v err=%v", input, err)
	}
	for _, raw := range []string{`{"prompt":"initial","extra":1}`, `{"prompt":"initial"}{}`, `{}`, `{"prompt":""}`} {
		if _, err := decodeEnvironmentRequest(raw); err == nil {
			t.Fatalf("accepted invalid environment request %q", raw)
		}
	}
}

func TestHTTPResultNotChangedByFailedRequest(t *testing.T) {
	commitSHAFile := filepath.Join(t.TempDir(), "commit-sha")
	const oldSHA = "0123456789abcdef0123456789abcdef01234567"
	const newSHA = "89abcdef0123456789abcdef0123456789abcdef"
	if err := os.WriteFile(commitSHAFile, []byte(oldSHA), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_COMMIT_SHA_FILE", commitSHAFile)

	runner := &httpFakeRunner{}
	h := newHTTPHandler(newSession(runner))
	first := httptest.NewRecorder()
	h.ServeHTTP(first, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"prompt":"first"}`)))
	if first.Code != http.StatusOK || !strings.Contains(first.Body.String(), oldSHA) {
		t.Fatalf("first status=%d body=%s", first.Code, first.Body.String())
	}
	if err := os.WriteFile(commitSHAFile, []byte(newSHA), 0o600); err != nil {
		t.Fatal(err)
	}
	runner.err = errors.New("prompt must not leak")
	failed := httptest.NewRecorder()
	h.ServeHTTP(failed, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"prompt":"secret-prompt"}`)))
	if strings.Contains(failed.Body.String(), "secret-prompt") || strings.Contains(failed.Body.String(), "prompt must not leak") || strings.Contains(failed.Body.String(), oldSHA) || strings.Contains(failed.Body.String(), newSHA) {
		t.Fatalf("error leaked sensitive data: %s", failed.Body.String())
	}
	got := httptest.NewRecorder()
	h.ServeHTTP(got, httptest.NewRequest(http.MethodGet, "/v1/result", nil))
	if got.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", got.Code, got.Body.String())
	}
	var result response
	if err := json.NewDecoder(got.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if result.ThreadID != "thread-1" || result.CommitSHA != oldSHA {
		t.Fatalf("result=%+v, want old commit_sha %q", result, oldSHA)
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

func TestHTTPResultIncludesCommitSHAFromAbsoluteAttemptFile(t *testing.T) {
	commitSHAFile := filepath.Join(t.TempDir(), "commit-sha")
	const firstSHA = "0123456789abcdef0123456789abcdef01234567"
	if err := os.WriteFile(commitSHAFile, []byte(firstSHA), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_COMMIT_SHA_FILE", commitSHAFile)

	h := newHTTPHandler(newSession(&httpFakeRunner{}))
	r := httptest.NewRecorder()
	h.ServeHTTP(r, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"prompt":"first"}`)))
	if r.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", r.Code, r.Body.String())
	}
	var got response
	if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.CommitSHA != firstSHA {
		t.Fatalf("commit_sha=%q, want %q", got.CommitSHA, firstSHA)
	}
}

func TestHTTPResultOmitsCommitSHAWhenFileIsMissingOrInvalid(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
	}{
		{name: "missing", content: ""},
		{name: "invalid", content: "not-a-commit"},
		{name: "wrong length", content: hex.EncodeToString([]byte("too short"))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			commitSHAFile := filepath.Join(t.TempDir(), "commit-sha")
			if tc.name != "missing" {
				if err := os.WriteFile(commitSHAFile, []byte(tc.content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("CODEX_COMMIT_SHA_FILE", commitSHAFile)

			h := newHTTPHandler(newSession(&httpFakeRunner{}))
			r := httptest.NewRecorder()
			h.ServeHTTP(r, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"prompt":"first"}`)))
			if r.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", r.Code, r.Body.String())
			}
			if strings.Contains(r.Body.String(), "commit_sha") {
				t.Fatalf("result unexpectedly contains commit_sha: %s", r.Body.String())
			}
		})
	}
}

func TestHTTPFollowUpRefreshesCommitSHA(t *testing.T) {
	commitSHAFile := filepath.Join(t.TempDir(), "commit-sha")
	const firstSHA = "0123456789abcdef0123456789abcdef01234567"
	const secondSHA = "89abcdef0123456789abcdef0123456789abcdef"
	if err := os.WriteFile(commitSHAFile, []byte(firstSHA), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_COMMIT_SHA_FILE", commitSHAFile)

	h := newHTTPHandler(newSession(&httpFakeRunner{}))
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

	first := post("first")
	if err := os.WriteFile(commitSHAFile, []byte(secondSHA), 0o600); err != nil {
		t.Fatal(err)
	}
	second := post("follow-up")
	if first.CommitSHA != firstSHA || second.CommitSHA != secondSHA {
		t.Fatalf("commit_sha values=%q/%q, want %q/%q", first.CommitSHA, second.CommitSHA, firstSHA, secondSHA)
	}
	get := httptest.NewRecorder()
	h.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/v1/result", nil))
	if get.Code != http.StatusOK {
		t.Fatalf("GET status=%d body=%s", get.Code, get.Body.String())
	}
	var latest response
	if err := json.NewDecoder(get.Body).Decode(&latest); err != nil {
		t.Fatal(err)
	}
	if latest.CommitSHA != secondSHA {
		t.Fatalf("GET commit_sha=%q, want %q", latest.CommitSHA, secondSHA)
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
