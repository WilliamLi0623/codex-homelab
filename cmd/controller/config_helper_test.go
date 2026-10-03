package main

import "testing"

func TestDisabledBootstrapRejectsHelperConfiguration(t *testing.T) {
	for _, key := range []string{"SESSION_BOOTSTRAP_HELPER_PATH", "SESSION_BOOTSTRAP_HELPER_SHA256"} {
		t.Run(key, func(t *testing.T) {
			setControllerEnvironment(t)
			t.Setenv(key, "configured")
			if _, err := loadSessionBootstrapConfig(true); err == nil {
				t.Fatal("disabled bootstrap silently accepted helper configuration")
			}
		})
	}
}

func TestSessionBootstrapRequiresPinnedHelper(t *testing.T) {
	for _, pin := range []string{"", "bad", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"} {
		t.Run(pin, func(t *testing.T) {
			t.Setenv("SESSION_BOOTSTRAP_ENABLED", "true")
			for _, key := range []string{"SESSION_SSH_MATERIAL_ROOT", "SESSION_BOOTSTRAP_BACKUP_ROOT", "SESSION_BOOTSTRAP_ARTIFACT_ROOT", "SESSION_BOOTSTRAP_HELPER_PATH"} {
				t.Setenv(key, "/var/lib/test")
			}
			t.Setenv("SESSION_SSH_KEYGEN", "/usr/bin/ssh-keygen")
			t.Setenv("SESSION_SSH_EXECUTABLE", "/usr/bin/ssh")
			t.Setenv("SESSION_BOOTSTRAP_CODEX_VERSION", "0.160.0")
			t.Setenv("SESSION_BOOTSTRAP_HELPER_SHA256", pin)
			if _, err := loadSessionBootstrapConfig(true); err == nil {
				t.Fatal("bootstrap accepted missing or invalid helper pin")
			}
		})
	}
}
