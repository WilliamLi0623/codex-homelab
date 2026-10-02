package sessionruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/WilliamLi0623/codex-homelab/internal/codexsession"
	"github.com/WilliamLi0623/codex-homelab/internal/store"
)

var errBootstrapTransportStage = errors.New("pinned Codex App Server transport could not be verified")

type bootstrapTransportProcess interface {
	Initialize(context.Context) error
	Close(context.Context) error
}

type bootstrapTransportStage struct {
	material *SSHMaterialRegistry
	resolve  bootstrapArtifactSSHConfigResolver
	open     func(context.Context, codexsession.SSHAppServerConfig) (bootstrapTransportProcess, error)
}

type bootstrapSSHAppServerProcess struct {
	process *codexsession.AppServerProcess
}

func (p bootstrapSSHAppServerProcess) Initialize(ctx context.Context) error {
	if p.process == nil || p.process.Client == nil {
		return errBootstrapTransportStage
	}
	return p.process.Client.Initialize(ctx)
}

func (p bootstrapSSHAppServerProcess) Close(ctx context.Context) error {
	if p.process == nil {
		return errBootstrapTransportStage
	}
	return p.process.Close(ctx)
}

func newBootstrapTransportStage(material *SSHMaterialRegistry, resolve bootstrapArtifactSSHConfigResolver) (*bootstrapTransportStage, error) {
	if material == nil || resolve == nil {
		return nil, ErrBootstrapConfiguration
	}
	return &bootstrapTransportStage{
		material: material,
		resolve:  resolve,
		open: func(ctx context.Context, config codexsession.SSHAppServerConfig) (bootstrapTransportProcess, error) {
			process, err := codexsession.StartSSHAppServerProcess(ctx, config)
			if err != nil {
				return nil, errBootstrapTransportStage
			}
			return bootstrapSSHAppServerProcess{process: process}, nil
		},
	}, nil
}

func (s *bootstrapTransportStage) Apply(ctx context.Context, binding store.SessionRuntimeBinding) (store.SessionBootstrapEvidence, error) {
	return s.verify(ctx, binding)
}

func (s *bootstrapTransportStage) Observe(ctx context.Context, binding store.SessionRuntimeBinding) (store.SessionBootstrapEvidence, bool, error) {
	evidence, err := s.verify(ctx, binding)
	return evidence, err == nil, err
}

func (s *bootstrapTransportStage) verify(ctx context.Context, binding store.SessionRuntimeBinding) (store.SessionBootstrapEvidence, error) {
	var empty store.SessionBootstrapEvidence
	if s == nil || s.material == nil || s.resolve == nil || s.open == nil || ctx == nil || !validMaterialBinding(binding) {
		return empty, errBootstrapTransportStage
	}
	if err := ctx.Err(); err != nil {
		return empty, errBootstrapTransportStage
	}
	material, err := s.material.Load(binding)
	if err != nil {
		return empty, errBootstrapTransportStage
	}
	config, err := s.resolve(ctx, binding, material)
	if err != nil {
		return empty, errBootstrapTransportStage
	}
	config.IdentityFile = material.IdentityFile
	config.KnownHostsFile = material.KnownHostsFile
	config.HostKeyAlias = material.Alias
	if expectedHome := bootstrapCodexHome(binding); expectedHome == "" || config.CodexHome != expectedHome {
		return empty, errBootstrapTransportStage
	}
	if _, _, _, err := codexsession.BuildSSHAppServerCommand(config, nil); err != nil {
		return empty, errBootstrapTransportStage
	}
	process, err := s.open(ctx, config)
	if err != nil || process == nil {
		return empty, errBootstrapTransportStage
	}
	initializeErr := process.Initialize(ctx)
	closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), bootstrapTransportCloseTimeout)
	closeErr := process.Close(closeCtx)
	cancel()
	if initializeErr != nil || closeErr != nil {
		return empty, errBootstrapTransportStage
	}
	data, err := json.Marshal(struct {
		Binding      store.SessionRuntimeBinding `json:"binding"`
		HostKeyAlias string                      `json:"host_key_alias"`
		CodexHome    string                      `json:"codex_home"`
		Handshake    string                      `json:"handshake"`
	}{materialBinding(binding), material.Alias, config.CodexHome, "initialize-initialized"})
	if err != nil {
		return empty, errBootstrapTransportStage
	}
	digest := sha256.Sum256(data)
	return store.SessionBootstrapEvidence{SHA256: hex.EncodeToString(digest[:])}, nil
}

const bootstrapTransportCloseTimeout = 5 * time.Second
