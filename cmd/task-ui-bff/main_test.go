package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLoadUIConfigDefaultsToPrivateListen(t *testing.T) {
	config, err := loadUIConfig(func(name string) string {
		return map[string]string{"TASK_UI_CONTROLLER_URL": "http://127.0.0.1:8080", "TASK_UI_AUTH_TOKEN": "operator-secret"}[name]
	}, "", "", "", "")
	if err != nil {
		t.Fatalf("loadUIConfig() error = %v", err)
	}
	if config.ListenAddress != defaultUIListenAddress {
		t.Fatalf("listen = %q, want %q", config.ListenAddress, defaultUIListenAddress)
	}
}

func TestUIBFFAuthenticatesAndAllowlistsControllerPaths(t *testing.T) {
	var receivedPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		receivedPath = request.URL.RequestURI()
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"tasks":[]}`)
	}))
	defer upstream.Close()
	config, err := loadUIConfig(func(name string) string {
		return map[string]string{"TASK_UI_CONTROLLER_URL": upstream.URL, "TASK_UI_AUTH_TOKEN": "operator-secret"}[name]
	}, "", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	handler := newUIHandler(config)
	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/ui/api/tasks", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d, want 401", unauthorized.Code)
	}
	authorizedRequest := httptest.NewRequest(http.MethodGet, "/ui/api/tasks?state=RUNNING", nil)
	authorizedRequest.Header.Set("Authorization", "Bearer operator-secret")
	authorized := httptest.NewRecorder()
	handler.ServeHTTP(authorized, authorizedRequest)
	if authorized.Code != http.StatusOK || receivedPath != "/v1/tasks?state=RUNNING" {
		t.Fatalf("authorized status=%d path=%q, want 200 and allowlisted upstream path", authorized.Code, receivedPath)
	}
	blockedRequest := httptest.NewRequest(http.MethodGet, "/ui/api/admin/secrets", nil)
	blockedRequest.Header.Set("Authorization", "Bearer operator-secret")
	blocked := httptest.NewRecorder()
	handler.ServeHTTP(blocked, blockedRequest)
	if blocked.Code != http.StatusNotFound {
		t.Fatalf("blocked status = %d, want 404", blocked.Code)
	}
}

func TestUIBFFRedactsControllerErrorsAndPreservesSSE(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if strings.HasSuffix(request.URL.Path, "/events/stream") {
			writer.Header().Set("Content-Type", "text/event-stream")
			writer.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(writer, "id: event-1\nevent: task.running\ndata: {}\n\n")
			return
		}
		writer.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(writer, `{"error":"secret upstream token","stack":"private"}`)
	}))
	defer upstream.Close()
	config, err := loadUIConfig(func(name string) string {
		return map[string]string{"TASK_UI_CONTROLLER_URL": upstream.URL, "TASK_UI_AUTH_TOKEN": "operator-secret"}[name]
	}, "", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	handler := newUIHandler(config)
	errorRequest := httptest.NewRequest(http.MethodGet, "/ui/api/tasks/task-1", nil)
	errorRequest.Header.Set("Authorization", "Bearer operator-secret")
	errorResponse := httptest.NewRecorder()
	handler.ServeHTTP(errorResponse, errorRequest)
	if errorResponse.Code != http.StatusBadGateway || strings.Contains(errorResponse.Body.String(), "secret") {
		t.Fatalf("error response = %d %q, want redacted upstream failure", errorResponse.Code, errorResponse.Body.String())
	}
	streamRequest := httptest.NewRequest(http.MethodGet, "/ui/api/tasks/task-1/events/stream", nil)
	streamRequest.Header.Set("Authorization", "Bearer operator-secret")
	streamResponse := httptest.NewRecorder()
	handler.ServeHTTP(streamResponse, streamRequest)
	if streamResponse.Code != http.StatusOK || streamResponse.Header().Get("Content-Type") != "text/event-stream" || !strings.Contains(streamResponse.Body.String(), "event: task.running") {
		t.Fatalf("stream response = %d %q, want SSE passthrough", streamResponse.Code, streamResponse.Body.String())
	}
}

func TestUIBFFConfigRejectsPublicListener(t *testing.T) {
	_, err := loadUIConfig(func(name string) string {
		return map[string]string{"TASK_UI_CONTROLLER_URL": "http://127.0.0.1:8080", "TASK_UI_AUTH_TOKEN": "secret"}[name]
	}, "0.0.0.0:8081", "", "", "")
	if err == nil || !strings.Contains(err.Error(), "private") {
		t.Fatalf("error = %v, want private-listener error", err)
	}
}
