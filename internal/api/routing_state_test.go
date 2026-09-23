package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/WilliamLi0623/codex-homelab/internal/store"
)

const testRoutingToken = "0123456789abcdef0123456789abcdef"

func TestRoutingStateEndpointRequiresDedicatedToken(t *testing.T) {
	database, err := store.Open(t.TempDir() + "/routing.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	server := NewServerWithRoutingStateToken(database, nil, nil, nil, nil, testRoutingToken)
	body := routingStateBody("normal", 1, time.Now().UTC())

	for _, token := range []string{"", "Bearer wrong-token"} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/internal/v1/routing-state", bytes.NewReader(body))
		if token != "" {
			request.Header.Set("Authorization", token)
		}
		server.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("token %q: status = %d, want 401", token, recorder.Code)
		}
		if strings.Contains(recorder.Body.String(), testRoutingToken) {
			t.Fatal("response disclosed configured bearer token")
		}
	}
}

func TestRoutingStateEndpointDisabledWithoutServerToken(t *testing.T) {
	server := newTestServer(t)
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/internal/v1/routing-state", strings.NewReader(`{}`)))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for disabled endpoint", recorder.Code)
	}
}

func TestRoutingStateEndpointAcceptsOnlyAuthenticatedValidTransitions(t *testing.T) {
	database, err := store.Open(t.TempDir() + "/routing.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	server := NewServerWithRoutingStateToken(database, nil, nil, nil, nil, testRoutingToken)
	observed := time.Now().UTC().Truncate(time.Millisecond)

	for _, transition := range []struct {
		mode       string
		generation int64
	}{
		{mode: "normal", generation: 1},
		{mode: "quota_fallback", generation: 2},
	} {
		body := routingStateBody(transition.mode, transition.generation, observed)
		recorder := postRoutingState(server, body)
		if recorder.Code != http.StatusOK {
			t.Fatalf("transition %+v status = %d, body = %s", transition, recorder.Code, recorder.Body.String())
		}
		var response struct {
			Mode       string `json:"mode"`
			Generation int64  `json:"generation"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if response.Mode != transition.mode || response.Generation != transition.generation {
			t.Fatalf("response = %+v, want mode=%s generation=%d", response, transition.mode, transition.generation)
		}
		if strings.Contains(recorder.Body.String(), "observed_at") || strings.Contains(recorder.Body.String(), testRoutingToken) {
			t.Fatalf("response contains disallowed detail: %s", recorder.Body.String())
		}
	}

	// Exact duplicate is idempotent.
	if recorder := postRoutingState(server, routingStateBody("quota_fallback", 2, observed)); recorder.Code != http.StatusOK {
		t.Fatalf("duplicate status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	state, err := database.GetRoutingState(context.Background())
	if err != nil || state.Mode != "quota_fallback" || state.Generation != 2 {
		t.Fatalf("persisted state = %+v, error = %v", state, err)
	}
}

func TestRoutingStateEndpointRejectsMalformedAndStaleRequests(t *testing.T) {
	database, err := store.Open(t.TempDir() + "/routing.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	server := NewServerWithRoutingStateToken(database, nil, nil, nil, nil, testRoutingToken)
	now := time.Now().UTC()
	valid := routingStateBody("normal", 2, now)
	invalid := [][]byte{
		[]byte(`{"mode":"quota_fallback","observed_at":"not-a-time","generation":1}`),
		[]byte(`{"mode":"provider_down","observed_at":"` + now.Format(time.RFC3339Nano) + `","generation":1}`),
		[]byte(`{"mode":"normal","observed_at":"` + now.Format(time.RFC3339Nano) + `","generation":1,"extra":true}`),
		routingStateBody("normal", 0, now),
		routingStateBody("normal", 1, now.Add(10*time.Minute)),
		routingStateBody("normal", 1, now.Add(-10*time.Minute)),
	}
	for i, body := range invalid {
		if recorder := postRoutingState(server, body); recorder.Code != http.StatusBadRequest {
			t.Fatalf("invalid case %d status = %d, body = %s", i, recorder.Code, recorder.Body.String())
		}
	}
	if recorder := postRoutingState(server, valid); recorder.Code != http.StatusOK {
		t.Fatalf("valid seed status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if recorder := postRoutingState(server, routingStateBody("quota_fallback", 1, now.Add(time.Second))); recorder.Code != http.StatusConflict {
		t.Fatalf("stale generation status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	oversized := []byte(`{"mode":"` + strings.Repeat("a", 5<<10) + `","observed_at":"` + now.Format(time.RFC3339Nano) + `","generation":3}`)
	if recorder := postRoutingState(server, oversized); recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized request status = %d, want 413", recorder.Code)
	}
	state, err := database.GetRoutingState(context.Background())
	if err != nil || state.Mode != "normal" || state.Generation != 2 {
		t.Fatalf("invalid request changed state: %+v, error = %v", state, err)
	}
}

func postRoutingState(server *Server, body []byte) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/internal/v1/routing-state", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+testRoutingToken)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	return recorder
}

func routingStateBody(mode string, generation int64, observed time.Time) []byte {
	return []byte(`{"mode":"` + mode + `","observed_at":"` + observed.Format(time.RFC3339Nano) + `","generation":` + strconv.FormatInt(generation, 10) + `}`)
}
