package sessionruntime

import (
	"context"
	"errors"
	"time"

	"github.com/WilliamLi0623/codex-homelab/internal/codexsession"
	"github.com/WilliamLi0623/codex-homelab/internal/store"
)

var errBootstrapHelperStage = errors.New("generation-pinned bootstrap helper could not be verified")

type bootstrapHelperStage struct {
	file, pin string
	material  *SSHMaterialRegistry
	resolve   bootstrapArtifactSSHConfigResolver
	install   func(context.Context, codexsession.SSHAppServerConfig, string, string, []byte) (string, error)
	observe   func(context.Context, codexsession.SSHAppServerConfig, string, string, int64) (string, error)
}

func newBootstrapHelperStage(file, pin string, material *SSHMaterialRegistry, resolve bootstrapArtifactSSHConfigResolver) (*bootstrapHelperStage, error) {
	if !validBootstrapPOSIXPath(file) || !bootstrapDigestPattern.MatchString(pin) || material == nil || resolve == nil {
		return nil, ErrBootstrapConfiguration
	}
	return &bootstrapHelperStage{file: file, pin: pin, material: material, resolve: resolve, install: codexsession.RunSSHBootstrapHelperInstall, observe: codexsession.RunSSHBootstrapHelperObserve}, nil
}

func (s *bootstrapHelperStage) prepare(ctx context.Context, binding store.SessionRuntimeBinding) (bootstrapHelperArtifact, codexsession.SSHAppServerConfig, string, error) {
	var artifact bootstrapHelperArtifact
	var config codexsession.SSHAppServerConfig
	if s == nil || ctx == nil || ctx.Err() != nil || !validMaterialBinding(binding) || s.material == nil || s.resolve == nil || s.install == nil || s.observe == nil {
		return artifact, config, "", errBootstrapHelperStage
	}
	artifact, err := verifyBootstrapHelperArtifact(ctx, s.file, s.pin)
	if err != nil {
		return bootstrapHelperArtifact{}, config, "", errBootstrapHelperStage
	}
	digest := bootstrapHelperGenerationDigest(binding, artifact)
	if digest == "" {
		return bootstrapHelperArtifact{}, config, "", errBootstrapHelperStage
	}
	material, err := s.material.Load(binding)
	if err != nil {
		return bootstrapHelperArtifact{}, config, "", errBootstrapHelperStage
	}
	config, err = s.resolve(ctx, binding, material)
	if err != nil || config.CodexHome != bootstrapCodexHome(binding) {
		return bootstrapHelperArtifact{}, codexsession.SSHAppServerConfig{}, "", errBootstrapHelperStage
	}
	config.IdentityFile, config.KnownHostsFile, config.HostKeyAlias = material.IdentityFile, material.KnownHostsFile, material.Alias
	return artifact, config, digest, nil
}

// Apply is permitted only under a freshly claimed durable helper checkpoint.
// A failed/ambiguous transfer is never retried here.
func (s *bootstrapHelperStage) Apply(ctx context.Context, binding store.SessionRuntimeBinding) (store.SessionBootstrapEvidence, error) {
	artifact, config, digest, err := s.prepare(ctx, binding)
	if err != nil {
		return store.SessionBootstrapEvidence{}, errBootstrapHelperStage
	}
	bounded, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	receipt, err := s.install(bounded, config, digest, artifact.SHA256, artifact.Bytes)
	if err != nil || receipt != digest {
		return store.SessionBootstrapEvidence{}, errBootstrapHelperStage
	}
	return store.SessionBootstrapEvidence{SHA256: digest}, nil
}

// Observe never installs, repairs, replaces or removes guest files.
func (s *bootstrapHelperStage) Observe(ctx context.Context, binding store.SessionRuntimeBinding) (store.SessionBootstrapEvidence, bool, error) {
	artifact, config, digest, err := s.prepare(ctx, binding)
	if err != nil {
		return store.SessionBootstrapEvidence{}, false, errBootstrapHelperStage
	}
	bounded, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	receipt, err := s.observe(bounded, config, digest, artifact.SHA256, int64(len(artifact.Bytes)))
	if err != nil || receipt != digest {
		return store.SessionBootstrapEvidence{}, false, errBootstrapHelperStage
	}
	return store.SessionBootstrapEvidence{SHA256: digest}, true, nil
}
