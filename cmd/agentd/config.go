package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ensureCodexConfig creates only non-secret, attempt-scoped provider settings.
// Credentials remain in the runtime environment/Secret and are never copied
// into CODEX_HOME, the worker image, or the repository.
func ensureCodexConfig(environment []string) error {
	values := environmentValues(environment)
	model := strings.TrimSpace(values["CODEX_MODEL"])
	wireAPI := strings.TrimSpace(values["CODEX_WIRE_API"])
	baseURL := strings.TrimSpace(values["CODEX_OPENAI_BASE_URL"])
	if model == "" && wireAPI == "" && baseURL == "" {
		return nil
	}
	if model == "" || wireAPI == "" || baseURL == "" {
		return errors.New("CODEX_MODEL, CODEX_WIRE_API, and CODEX_OPENAI_BASE_URL must be configured together")
	}
	if wireAPI == "coding-agent" {
		return errors.New("coding-agent protocol adapter is not implemented")
	}
	if wireAPI != "responses" {
		return errors.New("unsupported CODEX_WIRE_API")
	}
	home := strings.TrimSpace(values["CODEX_HOME"])
	attemptID := strings.TrimSpace(values["CODEX_ATTEMPT_ID"])
	if home == "" || !safeAttemptID(attemptID) || filepath.Base(filepath.Clean(home)) != attemptID || !filepath.IsAbs(home) {
		return errors.New("invalid CODEX_HOME for provider configuration")
	}
	if err := os.MkdirAll(home, 0700); err != nil {
		return fmt.Errorf("create CODEX_HOME: %w", err)
	}
	content := "model = " + strconv.Quote(model) + "\n" +
		"openai_base_url = " + strconv.Quote(strings.TrimRight(baseURL, "/")) + "\n" +
		"approval_policy = \"never\"\n" +
		"sandbox_mode = \"workspace-write\"\n"
	path := filepath.Join(home, "config.toml")
	if existing, err := os.ReadFile(path); err == nil {
		if string(existing) != content {
			return errors.New("attempt CODEX_HOME contains conflicting config.toml")
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read Codex config: %w", err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		return fmt.Errorf("write Codex config: %w", err)
	}
	return nil
}

func isToolLoopEnvironment(environment []string) bool {
	return isToolLoopValues(environmentValues(environment))
}

func isToolLoopValues(values map[string]string) bool {
	for _, key := range []string{"CODEX_MODEL", "CODEX_MODEL_PROFILE"} {
		model := strings.TrimSpace(values[key])
		if model == "muse-spark-1.3-contributor" || model == "glm-5.3-flash" {
			return true
		}
	}
	return false
}

func environmentValues(environment []string) map[string]string {
	values := make(map[string]string, len(environment))
	for _, entry := range environment {
		if name, value, ok := strings.Cut(entry, "="); ok {
			values[name] = value
		}
	}
	return values
}
