package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/WilliamLi0623/codex-homelab/internal/modelrouter"
	"github.com/WilliamLi0623/codex-homelab/internal/store"
)

func TestWorkerRoleMarkerMapsOnlyKnownLegacyProfiles(t *testing.T) {
	for _, value := range []string{"", "worker", "openai-primary", "muse-spark-1.3-contributor", "glm-5.3-flash"} {
		got, err := workerRoleMarker(value, "")
		if err != nil || got != "worker" {
			t.Errorf("workerRoleMarker(%q) = %q, %v; want worker", value, got, err)
		}
	}
	if _, err := workerRoleMarker("worker", "glm-5.3-flash"); err == nil {
		t.Fatal("conflicting role fields were accepted")
	}
	if _, err := workerRoleMarker("some-other-model", ""); err == nil {
		t.Fatal("unknown model selector was accepted")
	}
}

func TestRoutedAttemptsFreezeRoleAndQuotaModeBeforeDispatch(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "routing.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	server := NewServerWithRoutingStateAndRoutes(database, nil, nil, nil, nil, testRoutingToken, testAPIroutes(), true)
	taskID := createTestTask(t, server)
	postStart := func(taskID string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		server.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/tasks/"+taskID+"/attempts", bytes.NewBufferString(`{"model_profile":"worker"}`)))
		return recorder
	}
	initial := postStart(taskID)
	if initial.Code != http.StatusCreated {
		t.Fatalf("start without routing state = %d, body=%s", initial.Code, initial.Body.String())
	}
	var initialBody retryTaskResponse
	if err := json.Unmarshal(initial.Body.Bytes(), &initialBody); err != nil {
		t.Fatal(err)
	}
	initialRoute, err := database.GetAttemptRoute(context.Background(), taskID, initialBody.Attempt.ID)
	if err != nil || initialRoute.Mode != "normal" || initialRoute.Generation != 1 || initialRoute.Model != "gpt-6-luna" {
		t.Fatalf("initial route without persisted state=%+v err=%v; want normal generation 1", initialRoute, err)
	}
	taskID = createTaskWithKey(t, server, "routed-attempt-after-state")
	observed := time.Now().UTC().Truncate(time.Millisecond)
	if err := database.SetRoutingState(context.Background(), store.RoutingState{Mode: "normal", ObservedAt: observed, Generation: 4}); err != nil {
		t.Fatal(err)
	}
	started := postStart(taskID)
	if started.Code != http.StatusCreated {
		t.Fatalf("normal start = %d, body=%s", started.Code, started.Body.String())
	}
	var body retryTaskResponse
	if err := json.Unmarshal(started.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	snapshot, err := database.GetAttemptRoute(context.Background(), taskID, body.Attempt.ID)
	if err != nil || snapshot.Mode != "normal" || snapshot.Generation != 4 || snapshot.Model != "gpt-6-luna" || body.Attempt.ModelProfile != "worker" {
		t.Fatalf("normal attempt snapshot=%+v profile=%q err=%v", snapshot, body.Attempt.ModelProfile, err)
	}
	if err := database.SetRoutingState(context.Background(), store.RoutingState{Mode: "quota_fallback", ObservedAt: observed.Add(time.Second), Generation: 5}); err != nil {
		t.Fatal(err)
	}
	unchanged, err := database.GetAttemptRoute(context.Background(), taskID, body.Attempt.ID)
	if err != nil || unchanged.Mode != "normal" || unchanged.Generation != 4 {
		t.Fatalf("existing attempt route drifted: %+v, err=%v", unchanged, err)
	}
}

func testAPIroutes() modelrouter.RouteConfig {
	return modelrouter.RouteConfig{
		OpenAI: modelrouter.RouteSettings{Provider: "openai", Model: "gpt-6-luna", WireAPI: modelrouter.WireAPIResponses, ReasoningEffort: "high", BaseURL: "https://api.openai.com/v1", SecretName: "openai-route", SecretKey: "api-key"},
		Spark:  modelrouter.RouteSettings{Provider: "cch", Model: "muse-spark-1.3-contributor", WireAPI: modelrouter.WireAPIResponses, ReasoningEffort: "xhigh", BaseURL: "https://cch-jp.zenkexi.com/v1", SecretName: "cch-route", SecretKey: "api-key"},
		GLM:    modelrouter.RouteSettings{Provider: "cch", Model: "glm-5.3-flash", WireAPI: modelrouter.WireAPIChatCompletions, ReasoningEffort: "max", BaseURL: "https://cch-jp.zenkexi.com/v1", SecretName: "cch-route", SecretKey: "api-key"},
	}
}

func TestStartAttemptAcceptsModelProfileWorkerAndPersistsRole(t *testing.T) {
	server := newTestServer(t)
	taskID := createTestTask(t, server)
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/tasks/"+taskID+"/attempts", bytes.NewBufferString(`{"model_profile":"worker"}`)))
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response retryTaskResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Attempt.ModelProfile != "worker" {
		t.Fatalf("model_profile = %q, want worker", response.Attempt.ModelProfile)
	}
}
