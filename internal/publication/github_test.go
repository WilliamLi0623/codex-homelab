package publication

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGitHubClientFindsMatchingOpenPR(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/repos/owner/repo/pulls" {
			t.Fatalf("request = %s %s", r.Method, r.URL.String())
		}
		if got := r.URL.Query().Get("head"); got != "owner:codex/task-1" {
			t.Fatalf("head query = %q", got)
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{{"number": 3, "html_url": "https://example/pr/3", "head": map[string]string{"ref": "codex/task-1"}, "base": map[string]string{"ref": "main"}}})
	}))
	defer server.Close()

	pr, found, err := (GitHubClient{BaseURL: server.URL, HTTP: server.Client()}).FindOpenPullRequest(context.Background(), "owner/repo", "codex/task-1", "main")
	if err != nil || !found || pr.Number != 3 || pr.HeadRef != "codex/task-1" {
		t.Fatalf("FindOpenPullRequest() = %+v, %t, %v", pr, found, err)
	}
}

func TestGitHubClientCreatesPRWithToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/repos/owner/repo/pulls" {
			t.Fatalf("request = %s %s", r.Method, r.URL.String())
		}
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Fatalf("authorization header = %q", r.Header.Get("Authorization"))
		}
		var payload map[string]string
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload["head"] != "codex/task-1" || payload["base"] != "main" || payload["title"] != "title" {
			t.Fatalf("payload = %#v", payload)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"number":4,"html_url":"https://example/pr/4","head":{"ref":"codex/task-1"},"base":{"ref":"main"}}`))
	}))
	defer server.Close()

	pr, err := (GitHubClient{BaseURL: server.URL, Token: "secret", HTTP: server.Client()}).CreatePullRequest(context.Background(), "owner/repo", "codex/task-1", "main", "title", "body")
	if err != nil || pr.Number != 4 || !strings.HasSuffix(pr.URL, "/4") {
		t.Fatalf("CreatePullRequest() = %+v, %v", pr, err)
	}
}
