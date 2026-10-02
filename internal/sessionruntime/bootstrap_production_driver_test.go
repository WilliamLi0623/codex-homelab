package sessionruntime

import (
	"testing"
)

func TestNewBootstrapDriverRejectsIncompleteConfiguration(t *testing.T) {
	validPaths := BootstrapDriverConfig{
		SSHMaterialRoot: "/var/lib/codex-session/ssh",
		SSHKeygen:       "/usr/bin/ssh-keygen",
		BackupRoot:      "/var/lib/codex-session/backups",
		ArtifactRoot:    "/var/lib/codex-bootstrap-artifacts/codex-0.155.0-x86_64-unknown-linux-musl",
		SSHExecutable:   "/usr/bin/ssh",
	}
	for _, tc := range []struct {
		name   string
		config BootstrapDriverConfig
	}{
		{name: "nil runtime", config: validPaths},
		{name: "relative keygen", config: BootstrapDriverConfig{SSHMaterialRoot: validPaths.SSHMaterialRoot, SSHKeygen: "ssh-keygen", BackupRoot: validPaths.BackupRoot, ArtifactRoot: validPaths.ArtifactRoot, SSHExecutable: validPaths.SSHExecutable}},
		{name: "unclean material path", config: BootstrapDriverConfig{SSHMaterialRoot: "/var/lib/codex-session/../ssh", SSHKeygen: validPaths.SSHKeygen, BackupRoot: validPaths.BackupRoot, ArtifactRoot: validPaths.ArtifactRoot, SSHExecutable: validPaths.SSHExecutable}},
		{name: "missing artifact path", config: BootstrapDriverConfig{SSHMaterialRoot: validPaths.SSHMaterialRoot, SSHKeygen: validPaths.SSHKeygen, BackupRoot: validPaths.BackupRoot, SSHExecutable: validPaths.SSHExecutable}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var runtime *ProxmoxRuntime
			if tc.name != "nil runtime" {
				runtime = &ProxmoxRuntime{}
			}
			if _, err := NewBootstrapDriver(runtime, tc.config); err == nil {
				t.Fatal("NewBootstrapDriver() succeeded with incomplete or unsafe configuration")
			}
		})
	}
}
