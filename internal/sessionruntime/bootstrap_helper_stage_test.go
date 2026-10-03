package sessionruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/WilliamLi0623/codex-homelab/internal/codexsession"
	"github.com/WilliamLi0623/codex-homelab/internal/store"
)

func TestBootstrapHelperStageVerifiesCacheAndPinsGenerationWithoutRetry(t *testing.T) {
	registry, binding := materialRegistryFixture(t)
	if _, err := registry.Prepare(context.Background(), binding); err != nil {
		t.Fatal(err)
	}
	if err := registry.Pin(binding, publicMaterialFixture(t)); err != nil {
		t.Fatal(err)
	}
	material, err := registry.Load(binding)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "helper")
	data := []byte("isolated verified helper fixture")
	if err := os.WriteFile(file, data, 0700); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(data)
	pin := hex.EncodeToString(hash[:])
	resolve := func(context.Context, store.SessionRuntimeBinding, SSHMaterial) (codexsession.SSHAppServerConfig, error) {
		return codexsession.SSHAppServerConfig{SSHExecutable: "/usr/bin/ssh", Address: "192.0.2.40", CodexHome: bootstrapCodexHome(binding)}, nil
	}
	stage, err := newBootstrapHelperStage(file, pin, registry, resolve)
	if err != nil {
		t.Fatal(err)
	}
	installs, observations := 0, 0
	stage.install = func(_ context.Context, config codexsession.SSHAppServerConfig, digest, helperPin string, body []byte) (string, error) {
		installs++
		if config.IdentityFile != material.IdentityFile || config.KnownHostsFile != material.KnownHostsFile || config.HostKeyAlias != material.Alias || helperPin != pin || string(body) != string(data) {
			t.Fatal("helper lost verified bytes or generation-pinned SSH material")
		}
		return digest, nil
	}
	stage.observe = func(_ context.Context, _ codexsession.SSHAppServerConfig, digest, helperPin string, size int64) (string, error) {
		observations++
		if helperPin != pin || size != int64(len(data)) {
			t.Fatal("observer lost expected helper identity")
		}
		return digest, nil
	}
	evidence, err := stage.Apply(context.Background(), binding)
	if err != nil || !validBootstrapEvidence(evidence) {
		t.Fatalf("helper apply: %v", err)
	}
	readback, verified, err := stage.Observe(context.Background(), binding)
	if err != nil || !verified || readback != evidence || installs != 1 || observations != 1 {
		t.Fatalf("helper observation failed: %v", err)
	}
	stage.install = func(context.Context, codexsession.SSHAppServerConfig, string, string, []byte) (string, error) {
		installs++
		return "", errors.New("private upstream error")
	}
	if got, err := stage.Apply(context.Background(), binding); err == nil || got != (store.SessionBootstrapEvidence{}) || installs != 2 || err.Error() == "private upstream error" {
		t.Fatal("ambiguous helper transfer retried or exposed evidence/error")
	}
	stage.observe = func(context.Context, codexsession.SSHAppServerConfig, string, string, int64) (string, error) {
		observations++
		return "", errors.New("unknown")
	}
	if got, verified, err := stage.Observe(context.Background(), binding); err == nil || verified || got != (store.SessionBootstrapEvidence{}) || installs != 2 {
		t.Fatal("read-only UNKNOWN observation installed or claimed success")
	}
	if err := os.WriteFile(file, []byte("tampered helper"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := stage.Apply(context.Background(), binding); err == nil || installs != 2 {
		t.Fatal("tampered cache reached remote installer")
	}
}
