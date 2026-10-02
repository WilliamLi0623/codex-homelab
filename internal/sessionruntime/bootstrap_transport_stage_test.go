package sessionruntime

import (
	"context"
	"errors"
	"testing"

	"github.com/WilliamLi0623/codex-homelab/internal/codexsession"
	"github.com/WilliamLi0623/codex-homelab/internal/store"
)

type bootstrapTransportProcessFake struct {
	initializeErr error
	closeErr      error
	initialized   int
	closed        int
}

func (p *bootstrapTransportProcessFake) Initialize(context.Context) error {
	p.initialized++
	return p.initializeErr
}

func (p *bootstrapTransportProcessFake) Close(context.Context) error {
	p.closed++
	return p.closeErr
}

func TestBootstrapTransportStageInitializesPinnedAppServerAndClosesProcess(t *testing.T) {
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
	stage := &bootstrapTransportStage{
		material: registry,
		resolve: func(_ context.Context, got store.SessionRuntimeBinding, gotMaterial SSHMaterial) (codexsession.SSHAppServerConfig, error) {
			if got != binding || gotMaterial != material {
				t.Fatalf("resolver received unexpected binding/material: %+v %+v", got, gotMaterial)
			}
			return codexsession.SSHAppServerConfig{SSHExecutable: "/usr/bin/ssh", Address: "192.0.2.40", CodexHome: bootstrapCodexHome(binding)}, nil
		},
	}
	process := &bootstrapTransportProcessFake{}
	var got codexsession.SSHAppServerConfig
	stage.open = func(_ context.Context, config codexsession.SSHAppServerConfig) (bootstrapTransportProcess, error) {
		got = config
		return process, nil
	}

	evidence, err := stage.Apply(context.Background(), binding)
	if err != nil || !validBootstrapEvidence(evidence) {
		t.Fatalf("Apply() evidenceValid=%t error=%v", validBootstrapEvidence(evidence), err)
	}
	if process.initialized != 1 || process.closed != 1 {
		t.Fatalf("process Initialize/Close calls = %d/%d, want 1/1", process.initialized, process.closed)
	}
	if got.Address != "192.0.2.40" || got.HostKeyAlias != material.Alias || got.CodexHome != bootstrapCodexHome(binding) || got.IdentityFile != material.IdentityFile || got.KnownHostsFile != material.KnownHostsFile {
		t.Fatalf("opened App Server with unexpected SSH config: %+v", got)
	}
}

func TestBootstrapTransportStageDoesNotReportEvidenceWhenInitializeFails(t *testing.T) {
	registry, binding := materialRegistryFixture(t)
	if _, err := registry.Prepare(context.Background(), binding); err != nil {
		t.Fatal(err)
	}
	if err := registry.Pin(binding, publicMaterialFixture(t)); err != nil {
		t.Fatal(err)
	}
	stage := &bootstrapTransportStage{
		material: registry,
		resolve: func(context.Context, store.SessionRuntimeBinding, SSHMaterial) (codexsession.SSHAppServerConfig, error) {
			return codexsession.SSHAppServerConfig{SSHExecutable: "/usr/bin/ssh", Address: "192.0.2.40", CodexHome: bootstrapCodexHome(binding)}, nil
		},
	}
	process := &bootstrapTransportProcessFake{initializeErr: errors.New("private RPC detail")}
	stage.open = func(context.Context, codexsession.SSHAppServerConfig) (bootstrapTransportProcess, error) {
		return process, nil
	}

	evidence, verified, err := stage.Observe(context.Background(), binding)
	if err == nil || verified || evidence != (store.SessionBootstrapEvidence{}) {
		t.Fatalf("Observe() evidence=%+v verified=%t err=%v, want fail-closed", evidence, verified, err)
	}
	if process.initialized != 1 || process.closed != 1 {
		t.Fatalf("process Initialize/Close calls = %d/%d, want 1/1 after failed handshake", process.initialized, process.closed)
	}
	if got := err.Error(); got == "private RPC detail" {
		t.Fatal("transport stage exposed upstream RPC error detail")
	}
}
