package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/WilliamLi0623/codex-homelab/internal/api"
	"github.com/WilliamLi0623/codex-homelab/internal/capacity"
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

func TestNewHandlerFromEnvironmentConstructsOptionalSessionRuntime(t *testing.T) {
	proxmoxServer := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(proxmoxServer.Close)
	setControllerEnvironment(t)
	t.Setenv("PROXMOX_BASE_URL", proxmoxServer.URL)
	t.Setenv("KUBERNETES_BASE_URL", proxmoxServer.URL)
	t.Setenv("SESSION_RUNTIME_TEMPLATE_VMID", "3900")
	t.Setenv("SESSION_WORKSPACE_SIZE_GIB", "32")
	t.Setenv("SESSION_PROXMOX_TOKEN", "independent-session-proxmox-token")

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
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/sessions", nil))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("unauthenticated Session route status = %d, want 404", recorder.Code)
	}
}

func TestNewSessionProxmoxRuntimeUsesSessionToken(t *testing.T) {
	const sessionToken = "PVEAPIToken=session-user!session-v1=session-secret"
	const workerToken = "PVEAPIToken=worker-user!worker-v1=worker-secret"
	var authorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":4000}`))
	}))
	t.Cleanup(server.Close)

	runtime, err := newSessionProxmoxRuntime(environmentConfig{
		Proxmox:             capacity.ProxmoxConfig{BaseURL: server.URL, Node: "pve-node", Token: workerToken},
		SessionProxmoxToken: sessionToken,
	})
	if err != nil {
		t.Fatalf("newSessionProxmoxRuntime() error = %v", err)
	}
	available, err := runtime.TargetAvailable(context.Background(), 4000)
	if err != nil {
		t.Fatalf("TargetAvailable() error = %v", err)
	}
	if !available {
		t.Fatal("TargetAvailable() = false, want available")
	}
	if authorization != sessionToken {
		t.Fatalf("Session Proxmox Authorization = %q, want independent Session token", authorization)
	}
}
