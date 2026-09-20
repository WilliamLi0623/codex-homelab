package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureCodexConfigWritesNonSecretAttemptConfig(t *testing.T) {
	home := filepath.Join(t.TempDir(), "attempt-1")
	env := []string{
		"CODEX_HOME=" + home,
		"CODEX_ATTEMPT_ID=attempt-1",
		"CODEX_MODEL=glm-5.3-flash",
		"CODEX_OPENAI_BASE_URL=https://cch-jp.zenkexi.com/v1/",
		"OPENAI_API_KEY=must-not-be-written",
	}
	if err := ensureCodexConfig(env); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(home, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if !strings.Contains(content, `model = "glm-5.3-flash"`) || !strings.Contains(content, `openai_base_url = "https://cch-jp.zenkexi.com/v1"`) {
		t.Fatalf("config = %q", content)
	}
	if strings.Contains(content, "must-not-be-written") || strings.Contains(content, "OPENAI_API_KEY") {
		t.Fatalf("config contains secret material: %q", content)
	}
	if err := ensureCodexConfig(env); err != nil {
		t.Fatalf("idempotent repeat: %v", err)
	}
}

func TestEnsureCodexConfigRejectsConflictingConfigAndPartialProvider(t *testing.T) {
	home := filepath.Join(t.TempDir(), "attempt-2")
	env := []string{"CODEX_HOME=" + home, "CODEX_ATTEMPT_ID=attempt-2", "CODEX_MODEL=glm-5.3-flash"}
	if err := ensureCodexConfig(env); err == nil {
		t.Fatal("partial provider config succeeded")
	}
	good := append(env, "CODEX_OPENAI_BASE_URL=https://example.invalid/v1")
	if err := ensureCodexConfig(good); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte("model = \"other\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := ensureCodexConfig(good); err == nil {
		t.Fatal("conflicting config succeeded")
	}
}
