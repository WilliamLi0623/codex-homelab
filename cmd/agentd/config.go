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
	baseURL := strings.TrimSpace(values["CODEX_OPENAI_BASE_URL"])
	if model == "" && baseURL == "" {
		return nil
	}
	if model == "" || baseURL == "" {
		return errors.New("CODEX_MODEL and CODEX_OPENAI_BASE_URL must be configured together")
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
		"sandbox_mode = \"danger-full-access\"\n"
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

func environmentValues(environment []string) map[string]string {
	values := make(map[string]string, len(environment))
	for _, entry := range environment {
		if name, value, ok := strings.Cut(entry, "="); ok {
			values[name] = value
		}
	}
	return values
}
