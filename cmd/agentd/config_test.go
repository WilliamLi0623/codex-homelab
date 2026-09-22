package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/WilliamLi0623/codex-homelab/internal/muse"
)

func TestEnsureCodexConfigWritesNonSecretAttemptConfig(t *testing.T) {
	home := filepath.Join(t.TempDir(), "attempt-1")
	env := []string{
		"CODEX_HOME=" + home,
		"CODEX_ATTEMPT_ID=attempt-1",
		"CODEX_MODEL=openai-primary",
		"CODEX_WIRE_API=responses",
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
	if !strings.Contains(content, `model = "openai-primary"`) || !strings.Contains(content, `openai_base_url = "https://cch-jp.zenkexi.com/v1"`) {
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
	env := []string{"CODEX_HOME=" + home, "CODEX_ATTEMPT_ID=attempt-2", "CODEX_MODEL=openai-primary"}
	if err := ensureCodexConfig(env); err == nil {
		t.Fatal("partial provider config succeeded")
	}
	good := append(env, "CODEX_WIRE_API=responses", "CODEX_OPENAI_BASE_URL=https://example.invalid/v1")
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

func TestEnsureCodexConfigRejectsUnimplementedCodingAgentProtocol(t *testing.T) {
	home := filepath.Join(t.TempDir(), "attempt-glm")
	env := []string{
		"CODEX_HOME=" + home,
		"CODEX_ATTEMPT_ID=attempt-glm",
		"CODEX_MODEL=glm-5.3-flash",
		"CODEX_WIRE_API=coding-agent",
		"CODEX_OPENAI_BASE_URL=https://cch-jp.zenkexi.com/v1",
	}
	if err := ensureCodexConfig(env); err == nil || !strings.Contains(err.Error(), "coding-agent") {
		t.Fatalf("ensureCodexConfig() = %v; want coding-agent fail-closed error", err)
	}
}

func TestConfiguredRunnerSelectsMuseByExactProfile(t *testing.T) {
	workspace := t.TempDir()
	runner, err := newConfiguredRunner([]string{
		"CODEX_MODEL=openai-primary",
		"CODEX_MODEL_PROFILE=muse-spark-1.3-contributor",
		"CODEX_WIRE_API=responses",
		"CODEX_OPENAI_BASE_URL=https://cch.example/v1",
		"CODEX_API_KEY=secret",
		"CODEX_WORKSPACE=" + workspace,
	})
	if err != nil {
		t.Fatal(err)
	}
	museConfigured, ok := runner.(*museRunner)
	if !ok {
		t.Fatalf("runner type = %T; want *museRunner", runner)
	}
	if museConfigured.runner.ManualContinuation {
		t.Fatal("non-CCH endpoint unexpectedly enabled manual continuation")
	}
}

func TestConfiguredRunnerEnablesManualContinuationForCCH(t *testing.T) {
	workspace := t.TempDir()
	runner, err := newConfiguredRunner([]string{
		"CODEX_MODEL=muse-spark-1.3-contributor",
		"CODEX_WIRE_API=responses",
		"CODEX_OPENAI_BASE_URL=https://cch-jp.zenkexi.com/v1",
		"CODEX_API_KEY=secret",
		"CODEX_WORKSPACE=" + workspace,
	})
	if err != nil {
		t.Fatal(err)
	}
	museConfigured, ok := runner.(*museRunner)
	if !ok || !museConfigured.runner.ManualContinuation {
		t.Fatalf("runner = %#v; want CCH manual continuation", runner)
	}
	if museConfigured.runner.ReasoningEffort != "xhigh" {
		t.Fatalf("reasoning effort = %q, want xhigh", museConfigured.runner.ReasoningEffort)
	}
}

func TestConfiguredRunnerRejectsGLMResponsesWire(t *testing.T) {
	_, err := newConfiguredRunner([]string{
		"CODEX_MODEL=glm-5.3-flash",
		"CODEX_MODEL_PROFILE=glm-5.3-flash",
		"CODEX_WIRE_API=responses",
		"CODEX_OPENAI_BASE_URL=https://cch.example/v1",
		"CODEX_API_KEY=secret",
		"CODEX_WORKSPACE=/workspace/attempt-1",
	})
	if err == nil || !strings.Contains(err.Error(), "chat-completions") {
		t.Fatalf("newConfiguredRunner() = %v", err)
	}
}

func TestConfiguredRunnerSelectsGLMChatBridge(t *testing.T) {
	workspace := t.TempDir()
	runner, err := newConfiguredRunner([]string{
		"CODEX_MODEL=glm-5.3-flash",
		"CODEX_MODEL_PROFILE=glm-5.3-flash",
		"CODEX_WIRE_API=chat-completions",
		"CODEX_OPENAI_BASE_URL=https://cch-jp.zenkexi.com/v1",
		"CODEX_API_KEY=secret",
		"CODEX_WORKSPACE=" + workspace,
	})
	if err != nil {
		t.Fatal(err)
	}
	museConfigured, ok := runner.(*museRunner)
	if !ok {
		t.Fatalf("runner type = %T; want *museRunner", runner)
	}
	if _, ok := museConfigured.runner.Client.(*muse.ChatHTTPClient); !ok {
		t.Fatalf("client type = %T; want *muse.ChatHTTPClient", museConfigured.runner.Client)
	}
	if !museConfigured.runner.ManualContinuation {
		t.Fatal("Chat bridge must use manual continuation")
	}
	if museConfigured.runner.ReasoningEffort != "max" {
		t.Fatalf("reasoning effort = %q, want max", museConfigured.runner.ReasoningEffort)
	}
}

func TestConfiguredRunnerRejectsWrongModelReasoningEffort(t *testing.T) {
	_, err := newConfiguredRunner([]string{
		"CODEX_MODEL=glm-5.3-flash", "CODEX_MODEL_PROFILE=glm-5.3-flash", "CODEX_WIRE_API=chat-completions",
		"CODEX_OPENAI_BASE_URL=https://cch.example/v1", "CODEX_API_KEY=secret", "CODEX_WORKSPACE=/workspace/attempt-1",
		"CODEX_MODEL_REASONING_EFFORT=xhigh",
	})
	if err == nil || !strings.Contains(err.Error(), "reasoning effort") {
		t.Fatalf("newConfiguredRunner() = %v; want reasoning effort validation", err)
	}
}
