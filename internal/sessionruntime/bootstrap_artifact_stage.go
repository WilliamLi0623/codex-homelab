package sessionruntime

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"time"

	"github.com/WilliamLi0623/codex-homelab/internal/codexsession"
	"github.com/WilliamLi0623/codex-homelab/internal/store"
)

var errBootstrapArtifactStage = errors.New("bootstrap Codex artifact stage could not be verified")

type bootstrapArtifactSSHConfigResolver func(context.Context, store.SessionRuntimeBinding, SSHMaterial) (codexsession.SSHAppServerConfig, error)

// bootstrapArtifactStage performs one generation-bound pinned SSH upload and
// offers read-only reconciliation. Its caller must hold the durable stage
// claim; this adapter never accesses Store or retries a transfer.
type bootstrapArtifactStage struct {
	root     string
	material *SSHMaterialRegistry
	bundle   codexArtifactBundle
	resolve  bootstrapArtifactSSHConfigResolver
}

func newBootstrapArtifactStage(root, version string, material *SSHMaterialRegistry, resolve bootstrapArtifactSSHConfigResolver) (*bootstrapArtifactStage, error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || material == nil || resolve == nil {
		return nil, ErrBootstrapConfiguration
	}
	bundle, err := bootstrapCodexBundleForVersion(version)
	if err != nil {
		return nil, ErrBootstrapConfiguration
	}
	return &bootstrapArtifactStage{root: root, material: material, bundle: bundle, resolve: resolve}, nil
}

func (s *bootstrapArtifactStage) Apply(ctx context.Context, binding store.SessionRuntimeBinding) (store.SessionBootstrapEvidence, error) {
	var empty store.SessionBootstrapEvidence
	if s == nil || ctx == nil || !validMaterialBinding(binding) || s.material == nil || s.resolve == nil {
		return empty, errBootstrapArtifactStage
	}
	verified, err := verifyBootstrapArtifactBundle(ctx, s.root, s.bundle)
	if err != nil {
		return empty, errBootstrapArtifactStage
	}
	generationDigest := bootstrapCodexGenerationArtifactDigest(binding, s.bundle, verified)
	if generationDigest == "" {
		return empty, errBootstrapArtifactStage
	}
	material, err := s.material.Load(binding)
	if err != nil {
		return empty, errBootstrapArtifactStage
	}
	config, err := s.resolve(ctx, binding, material)
	if err != nil {
		return empty, errBootstrapArtifactStage
	}
	config.IdentityFile = material.IdentityFile
	config.KnownHostsFile = material.KnownHostsFile
	config.HostKeyAlias = material.Alias

	transferCtx, cancel := context.WithTimeout(ctx, bootstrapArtifactTransferTimeout)
	defer cancel()
	reader, writer := io.Pipe()
	writeResult := make(chan error, 1)
	go func() {
		writeErr := writeBootstrapArtifactTransfer(transferCtx, s.root, s.bundle, verified, writer)
		if writeErr != nil {
			_ = writer.CloseWithError(writeErr)
		} else {
			_ = writer.Close()
		}
		writeResult <- writeErr
	}()
	remoteEvidence, remoteErr := codexsession.RunSSHArtifactInstallWithEvidence(transferCtx, config, generationDigest, reader)
	_ = reader.Close()
	writeErr := <-writeResult
	if remoteErr != nil || writeErr != nil {
		return empty, errBootstrapArtifactStage
	}
	entries, err := bootstrapArtifactEntries(s.root, s.bundle)
	if err != nil {
		return empty, errBootstrapArtifactStage
	}
	expected := bootstrapCodexInstallEvidence(generationDigest, s.bundle, makeBootstrapCodexManifest(s.bundle, entries))
	if remoteEvidence != expected.SHA256 {
		return empty, errBootstrapArtifactStage
	}
	return expected, nil
}

func (s *bootstrapArtifactStage) Observe(ctx context.Context, binding store.SessionRuntimeBinding) (store.SessionBootstrapEvidence, bool, error) {
	var empty store.SessionBootstrapEvidence
	if s == nil || ctx == nil || !validMaterialBinding(binding) || s.material == nil || s.resolve == nil {
		return empty, false, errBootstrapArtifactStage
	}
	verified, err := verifyBootstrapArtifactBundle(ctx, s.root, s.bundle)
	if err != nil {
		return empty, false, errBootstrapArtifactStage
	}
	generationDigest := bootstrapCodexGenerationArtifactDigest(binding, s.bundle, verified)
	if generationDigest == "" {
		return empty, false, errBootstrapArtifactStage
	}
	material, err := s.material.Load(binding)
	if err != nil {
		return empty, false, errBootstrapArtifactStage
	}
	config, err := s.resolve(ctx, binding, material)
	if err != nil {
		return empty, false, errBootstrapArtifactStage
	}
	config.IdentityFile = material.IdentityFile
	config.KnownHostsFile = material.KnownHostsFile
	config.HostKeyAlias = material.Alias
	remoteEvidence, err := codexsession.RunSSHArtifactObserve(ctx, config, generationDigest)
	if err != nil {
		return empty, false, errBootstrapArtifactStage
	}
	entries, err := bootstrapArtifactEntries(s.root, s.bundle)
	if err != nil {
		return empty, false, errBootstrapArtifactStage
	}
	expected := bootstrapCodexInstallEvidence(generationDigest, s.bundle, makeBootstrapCodexManifest(s.bundle, entries))
	if remoteEvidence != expected.SHA256 {
		return empty, false, errBootstrapArtifactStage
	}
	return expected, true, nil
}

const bootstrapArtifactTransferTimeout = 10 * time.Minute
