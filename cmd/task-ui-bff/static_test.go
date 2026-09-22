package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestUIBFFServesOptionalStaticDirectoryWithoutExposingAPIToken(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "index.html"), []byte("<title>Codex task console</title>"), 0o644); err != nil {
		t.Fatalf("write static fixture: %v", err)
	}
	config, err := loadUIConfig(func(name string) string {
		return map[string]string{"TASK_UI_CONTROLLER_URL": "http://127.0.0.1:18080", "TASK_UI_AUTH_TOKEN": "operator-secret"}[name]
	}, "", "", "", "", directory)
	if err != nil {
		t.Fatalf("loadUIConfig() error = %v", err)
	}
	response := httptest.NewRecorder()
	newUIHandler(config).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
	if response.Code != http.StatusOK || response.Body.String() != "<title>Codex task console</title>" {
		t.Fatalf("static response = %d %q, want fixture", response.Code, response.Body.String())
	}
}
