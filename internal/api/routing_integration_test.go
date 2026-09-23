package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/WilliamLi0623/codex-homelab/internal/codexrouting"
	"github.com/WilliamLi0623/codex-homelab/internal/modelrouter"
	"github.com/WilliamLi0623/codex-homelab/internal/store"
)

type integrationQuotaReader struct {
	items []codexrouting.QuotaSnapshot
	err   error
}

func (r *integrationQuotaReader) Read(context.Context) (codexrouting.QuotaSnapshot, error) {
	if r.err != nil {
		return codexrouting.QuotaSnapshot{}, r.err
	}
	if len(r.items) == 0 {
		return codexrouting.QuotaSnapshot{}, errors.New("no quota fixture")
	}
	item := r.items[0]
	r.items = r.items[1:]
	return item, nil
}

func TestQuotaCoordinatorToFrozenAttemptRouteIntegration(t *testing.T) {
	base := newTestServer(t)
	dispatcher := &fakeDispatcher{}
	server := NewServerWithRoutingStateAndRoutes(base.store, dispatcher, nil, nil, nil, testRoutingToken, testAPIroutes(), true)
	httpServer := httptest.NewServer(server)
	defer httpServer.Close()
	allowed := func(value bool) *bool { return &value }
	reader := &integrationQuotaReader{items: []codexrouting.QuotaSnapshot{
		{OrdinaryUsageAllowed: allowed(false)},
		{OrdinaryUsageAllowed: allowed(true)},
	}}
	stateStore := &codexrouting.FileRoutingStateStore{Path: filepath.Join(t.TempDir(), "routing-state.json")}
	sink := codexrouting.HTTPModeSink{URL: httpServer.URL + "/internal/v1/routing-state", Token: testRoutingToken, Client: httpServer.Client()}
	coordinator, err := codexrouting.NewCoordinator(reader, sink, stateStore, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.PollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	firstTask := createTestTask(t, server)
	first := startIntegrationAttempt(t, server, firstTask)
	fallback, err := base.store.GetAttemptRoute(context.Background(), firstTask, first)
	if err != nil || fallback.Mode != "quota_fallback" || fallback.Generation != 1 || fallback.Provider != "cch" || fallback.Model != "glm-5.3-flash" || fallback.ReasoningEffort != "max" {
		t.Fatalf("fallback route=%+v err=%v", fallback, err)
	}
	if err := coordinator.PollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	// The coordinator's generation 2 is authoritative; that global transition
	// must not rewrite the already-started fallback attempt.
	current, err := base.store.GetRoutingState(context.Background())
	if err != nil || current.Mode != "normal" || current.Generation != 2 {
		t.Fatalf("controller routing state=%+v err=%v", current, err)
	}
	dispatch := httptest.NewRecorder()
	requestBody, _ := json.Marshal(map[string]any{"attempt_id": first, "prompt": "run harmless tests", "validation_command": []string{"go", "test", "./..."}})
	server.ServeHTTP(dispatch, httptest.NewRequest(http.MethodPost, "/v1/tasks/"+firstTask+"/dispatch", bytes.NewReader(requestBody)))
	if dispatch.Code != http.StatusAccepted {
		t.Fatalf("dispatch status=%d body=%s", dispatch.Code, dispatch.Body.String())
	}
	if len(dispatcher.requests) != 1 || dispatcher.requests[0].Route == nil || string(dispatcher.requests[0].Route.Mode) != string(codexrouting.ModeQuotaFallback) || dispatcher.requests[0].Route.Model != "glm-5.3-flash" || dispatcher.requests[0].Route.ReasoningEffort != "max" {
		t.Fatalf("dispatched route=%+v", dispatcher.requests)
	}
	secondTask := createTaskWithKey(t, server, "routing-normal-task")
	second := startIntegrationAttempt(t, server, secondTask)
	normal, err := base.store.GetAttemptRoute(context.Background(), secondTask, second)
	if err != nil || normal.Mode != "normal" || normal.Generation != 2 || normal.Provider != "openai" || normal.Model != "gpt-6-luna" || normal.ReasoningEffort != "high" {
		t.Fatalf("normal route=%+v err=%v", normal, err)
	}
	reader.err = errors.New("upstream HTTP 503")
	if err := coordinator.PollOnce(context.Background()); err == nil {
		t.Fatal("expected quota read failure")
	}
	state, err := base.store.GetRoutingState(context.Background())
	if err != nil || state.Mode != "normal" || state.Generation != 2 {
		t.Fatalf("provider 503 changed routing state: %+v err=%v", state, err)
	}
}

func TestFirstAttemptUsesConfiguredNormalRouteWhenStateIsMissing(t *testing.T) {
	base := newTestServer(t)
	server := NewServerWithRoutingStateAndRoutes(base.store, &fakeDispatcher{}, nil, nil, nil, testRoutingToken, testAPIroutes(), true)
	taskID := createTaskWithKey(t, server, "first-attempt-default-normal")
	attemptID := startIntegrationAttempt(t, server, taskID)

	route, err := base.store.GetAttemptRoute(context.Background(), taskID, attemptID)
	if err != nil {
		t.Fatalf("GetAttemptRoute() error = %v", err)
	}
	if route.Mode != "normal" || route.Generation != 1 || route.Provider != "openai" || route.Model != "gpt-6-luna" || route.WireAPI != "responses" || route.ReasoningEffort != "high" {
		t.Fatalf("first attempt route = %+v, want configured normal route at generation 1", route)
	}
}

func TestFirstAttemptStillFailsClosedWithoutNormalRouteConfiguration(t *testing.T) {
	base := newTestServer(t)
	server := NewServerWithRoutingStateAndRoutes(base.store, nil, nil, nil, nil, testRoutingToken, modelrouter.RouteConfig{}, true)
	taskID := createTaskWithKey(t, server, "first-attempt-no-normal-route")
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/tasks/"+taskID+"/attempts", bytes.NewBufferString(`{"profile":"worker"}`)))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("start attempt status = %d, want %d; body=%s", recorder.Code, http.StatusServiceUnavailable, recorder.Body.String())
	}
}

func TestDispatchUsesFrozenRouteAfterProviderConfigurationChanges(t *testing.T) {
	base := newTestServer(t)
	dispatcher := &fakeDispatcher{}
	originalConfig := testAPIroutes()
	serverAtCreation := NewServerWithRoutingStateAndRoutes(base.store, dispatcher, nil, nil, nil, testRoutingToken, originalConfig, true)
	if err := base.store.SetRoutingState(context.Background(), store.RoutingState{Mode: "normal", ObservedAt: time.Now().UTC(), Generation: 7}); err != nil {
		t.Fatal(err)
	}
	taskID := createTaskWithKey(t, serverAtCreation, "frozen-route-before-config-change")
	attemptID := startIntegrationAttempt(t, serverAtCreation, taskID)
	original, err := base.store.GetAttemptRoute(context.Background(), taskID, attemptID)
	if err != nil {
		t.Fatal(err)
	}
	rotatedConfig := originalConfig
	rotatedConfig.OpenAI.BaseURL = "https://rotated.example/v1"
	rotatedConfig.OpenAI.SecretName = "rotated-openai-route"
	serverAfterConfigChange := NewServerWithRoutingStateAndRoutes(base.store, dispatcher, nil, nil, nil, testRoutingToken, rotatedConfig, true)
	body, _ := json.Marshal(map[string]any{"attempt_id": attemptID, "prompt": "run harmless tests", "validation_command": []string{"go", "test", "./..."}})
	recorder := httptest.NewRecorder()
	serverAfterConfigChange.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/tasks/"+taskID+"/dispatch", bytes.NewReader(body)))
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("dispatch status=%d body=%s; want frozen attempt route to remain dispatchable", recorder.Code, recorder.Body.String())
	}
	if len(dispatcher.requests) != 1 || dispatcher.requests[0].Route == nil || dispatcher.requests[0].Route.BaseURL != original.BaseURL || dispatcher.requests[0].Route.SecretName != original.SecretName || dispatcher.requests[0].Route.Generation != 7 {
		t.Fatalf("dispatch route=%+v; want original frozen route %+v", dispatcher.requests, original)
	}
}

func startIntegrationAttempt(t *testing.T, server http.Handler, taskID string) string {
	t.Helper()
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/tasks/"+taskID+"/attempts", bytes.NewBufferString(`{"model_profile":"worker"}`)))
	if recorder.Code != http.StatusCreated {
		t.Fatalf("start attempt status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response retryTaskResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	return response.Attempt.ID
}

func createTaskWithKey(t *testing.T, server *Server, key string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"repository": "owner/repository", "base_ref": "main", "objective": "route integration", "idempotency_key": key})
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/tasks", bytes.NewReader(body)))
	if recorder.Code != http.StatusCreated {
		t.Fatalf("create task status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response createTaskResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	return response.Task.ID
}
