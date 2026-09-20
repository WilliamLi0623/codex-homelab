package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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

type testDispatcher struct{}

func (testDispatcher) Dispatch(context.Context, orchestrator.Request) (orchestrator.Dispatch, error) {
	return orchestrator.Dispatch{}, nil
}

var _ api.Dispatcher = testDispatcher{}
