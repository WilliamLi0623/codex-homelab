package main

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
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
