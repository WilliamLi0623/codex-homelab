package sessionruntime

import (
	"context"
	"testing"

	"github.com/WilliamLi0623/codex-homelab/internal/codexsession"
	"github.com/WilliamLi0623/codex-homelab/internal/store"
)

func TestNewBootstrapDriverRejectsIncompleteConfiguration(t *testing.T) {
	validPaths := BootstrapDriverConfig{
		SSHMaterialRoot:    "/var/lib/codex-session/ssh",
		SSHKeygen:          "/usr/bin/ssh-keygen",
		BackupRoot:         "/var/lib/codex-session/backups",
		ArtifactRoot:       "/var/lib/codex-bootstrap-artifacts",
		HelperArtifactPath: "/var/lib/codex-bootstrap-helpers/installer",
		HelperSHA256:       "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		CodexVersion:       "0.160.0",
		SSHExecutable:      "/usr/bin/ssh",
	}
	for _, tc := range []struct {
		name   string
		config BootstrapDriverConfig
	}{
		{name: "nil runtime", config: validPaths},
		{name: "relative keygen", config: BootstrapDriverConfig{SSHMaterialRoot: validPaths.SSHMaterialRoot, SSHKeygen: "ssh-keygen", BackupRoot: validPaths.BackupRoot, ArtifactRoot: validPaths.ArtifactRoot, CodexVersion: validPaths.CodexVersion, SSHExecutable: validPaths.SSHExecutable}},
		{name: "unclean material path", config: BootstrapDriverConfig{SSHMaterialRoot: "/var/lib/codex-session/../ssh", SSHKeygen: validPaths.SSHKeygen, BackupRoot: validPaths.BackupRoot, ArtifactRoot: validPaths.ArtifactRoot, CodexVersion: validPaths.CodexVersion, SSHExecutable: validPaths.SSHExecutable}},
		{name: "missing artifact path", config: BootstrapDriverConfig{SSHMaterialRoot: validPaths.SSHMaterialRoot, SSHKeygen: validPaths.SSHKeygen, BackupRoot: validPaths.BackupRoot, SSHExecutable: validPaths.SSHExecutable}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Keep the new prerequisite valid so existing cases exercise their
			// named failure, rather than all failing for an absent helper pin.
			tc.config.HelperArtifactPath = validPaths.HelperArtifactPath
			tc.config.HelperSHA256 = validPaths.HelperSHA256
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

func TestBootstrapArtifactStageUsesExplicitCodexVersion(t *testing.T) {
	material := &SSHMaterialRegistry{}
	root := t.TempDir()
	resolve := func(context.Context, store.SessionRuntimeBinding, SSHMaterial) (codexsession.SSHAppServerConfig, error) {
		return codexsession.SSHAppServerConfig{}, nil
	}
	for _, version := range []string{"0.155.0", "0.160.0"} {
		stage, err := newBootstrapArtifactStage(root, version, material, resolve)
		if err != nil {
			t.Fatalf("newBootstrapArtifactStage(%s) error = %v", version, err)
		}
		if stage.bundle.version != version {
			t.Fatalf("stage bundle version = %q, want %q", stage.bundle.version, version)
		}
	}
	if _, err := newBootstrapArtifactStage(root, "0.159.3", material, resolve); err == nil {
		t.Fatal("unsupported CLI version was accepted")
	}
}
