package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/WilliamLi0623/codex-homelab/internal/api"
	"github.com/WilliamLi0623/codex-homelab/internal/orchestrator"
)

func TestNewHandlerServesControllerHealth(t *testing.T) {
	handler, closeStore, err := newHandler(filepath.Join(t.TempDir(), "controller.sqlite"))
	if err != nil {
		t.Fatalf("newHandler() error = %v", err)
	}
	t.Cleanup(closeStore)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/health", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("health status = %d, want %d", recorder.Code, http.StatusOK)
	}
}

func TestNewHandlerFailsClosedWhenDispatcherIsNotConfigured(t *testing.T) {
	handler, closeStore, err := newHandler(filepath.Join(t.TempDir(), "controller.sqlite"))
	if err != nil {
		t.Fatalf("newHandler() error = %v", err)
	}
	t.Cleanup(closeStore)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/ready", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("readiness status = %d, want %d", recorder.Code, http.StatusServiceUnavailable)
	}
}

func TestNewHandlerWithDispatcherServesReady(t *testing.T) {
	handler, closeStore, err := newHandlerWithDispatcher(filepath.Join(t.TempDir(), "controller.sqlite"), testDispatcher{})
	if err != nil {
		t.Fatalf("newHandlerWithDispatcher() error = %v", err)
	}
	t.Cleanup(closeStore)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/ready", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("readiness status = %d, want %d", recorder.Code, http.StatusOK)
	}
}

func TestNewHandlerFromEnvironmentFailsClosedWithoutLeakingSecrets(t *testing.T) {
	setControllerEnvironment(t)
	const token = "production-secret-token"
	t.Setenv("PROXMOX_TOKEN", token)
	t.Setenv("KUBERNETES_BASE_URL", "")

	handler, closeStore, err := newHandlerFromEnvironment(filepath.Join(t.TempDir(), "controller.sqlite"))
	if err != nil {
		t.Fatalf("newHandlerFromEnvironment() error = %v", err)
	}
	t.Cleanup(closeStore)

	for _, path := range []string{"/v1/health", "/v1/ready"} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if path == "/v1/health" && recorder.Code != http.StatusOK {
			t.Fatalf("health status = %d, want %d", recorder.Code, http.StatusOK)
		}
		if path == "/v1/ready" && recorder.Code != http.StatusServiceUnavailable {
			t.Fatalf("readiness status = %d, want %d", recorder.Code, http.StatusServiceUnavailable)
		}
		if strings.Contains(recorder.Body.String(), token) {
			t.Fatalf("%s response leaked token", path)
		}
	}
}

func TestNewHandlerFromEnvironmentServesReadyWithCompleteConfig(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(server.Close)
	setControllerEnvironment(t)
	t.Setenv("PROXMOX_BASE_URL", server.URL)
	t.Setenv("KUBERNETES_BASE_URL", server.URL)
	const proxmoxToken = "production-proxmox-token"
	const kubernetesToken = "production-kubernetes-token"
	t.Setenv("PROXMOX_TOKEN", proxmoxToken)
	t.Setenv("KUBERNETES_TOKEN", kubernetesToken)

	handler, closeStore, err := newHandlerFromEnvironment(filepath.Join(t.TempDir(), "controller.sqlite"))
	if err != nil {
		t.Fatalf("newHandlerFromEnvironment() error = %v", err)
	}
	t.Cleanup(closeStore)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/ready", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("readiness status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), proxmoxToken) || strings.Contains(recorder.Body.String(), kubernetesToken) {
		t.Fatal("readiness response leaked a token")
	}
}

type testDispatcher struct{}

func (testDispatcher) Dispatch(context.Context, orchestrator.Request) (orchestrator.Dispatch, error) {
	return orchestrator.Dispatch{}, nil
}

var _ api.Dispatcher = testDispatcher{}
