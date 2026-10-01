package sessionruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"

	"github.com/WilliamLi0623/codex-homelab/internal/store"
)

var errBootstrapImageStage = errors.New("session image backup or sanitation could not be verified")

// These adapters never claim or complete checkpoints. The coordinator must
// first persist the matching fresh stage claim before invoking Apply.
type bootstrapImageBackupStage struct {
	backups *bootstrapBackupStore
	export  func(context.Context, store.SessionRuntimeBinding, io.Writer) (bootstrapTLSArchiveEvidence, error)
}

func newBootstrapImageBackupStage(runtime *ProxmoxRuntime, backups *bootstrapBackupStore) (*bootstrapImageBackupStage, error) {
	if runtime == nil || backups == nil {
		return nil, ErrBootstrapConfiguration
	}
	return &bootstrapImageBackupStage{backups: backups, export: runtime.exportBootstrapSessionImageArchive}, nil
}

func (s *bootstrapImageBackupStage) Apply(ctx context.Context, binding store.SessionRuntimeBinding) (store.SessionBootstrapEvidence, error) {
	if s == nil || s.backups == nil || s.export == nil || ctx == nil || !validMaterialBinding(binding) {
		return store.SessionBootstrapEvidence{}, errBootstrapImageStage
	}
	_, err := s.backups.save(ctx, binding, func(ctx context.Context, binding store.SessionRuntimeBinding, writer io.Writer) (bootstrapTLSArchiveEvidence, error) {
		return s.export(ctx, binding, writer)
	})
	if err != nil {
		return store.SessionBootstrapEvidence{}, errBootstrapImageStage
	}
	evidence, verified, err := s.backups.observePolicy(ctx, binding, bootstrapArchivePolicySessionImageCredentialsV2)
	if err != nil || !verified {
		return store.SessionBootstrapEvidence{}, errBootstrapImageStage
	}
	return evidence, nil
}

func (s *bootstrapImageBackupStage) Observe(ctx context.Context, binding store.SessionRuntimeBinding) (store.SessionBootstrapEvidence, bool, error) {
	if s == nil || s.backups == nil || ctx == nil || !validMaterialBinding(binding) {
		return store.SessionBootstrapEvidence{}, false, errBootstrapImageStage
	}
	return s.backups.observePolicy(ctx, binding, bootstrapArchivePolicySessionImageCredentialsV2)
}

type bootstrapImageSanitizedStage struct {
	backups *bootstrapBackupStore
	apply   func(context.Context, store.SessionRuntimeBinding) (bootstrapImageSanitationResult, error)
	observe func(context.Context, store.SessionRuntimeBinding) (bootstrapImageSanitationResult, error)
}

func newBootstrapImageSanitizedStage(runtime *ProxmoxRuntime, backups *bootstrapBackupStore) (*bootstrapImageSanitizedStage, error) {
	if runtime == nil || backups == nil {
		return nil, ErrBootstrapConfiguration
	}
	return &bootstrapImageSanitizedStage{backups: backups, apply: runtime.sanitizeBootstrapImage, observe: runtime.observeBootstrapImageSanitation}, nil
}

func (s *bootstrapImageSanitizedStage) Apply(ctx context.Context, binding store.SessionRuntimeBinding) (store.SessionBootstrapEvidence, error) {
	if s == nil || s.backups == nil || s.apply == nil || ctx == nil || !validMaterialBinding(binding) {
		return store.SessionBootstrapEvidence{}, errBootstrapImageStage
	}
	backup, verified, err := s.backups.observePolicy(ctx, binding, bootstrapArchivePolicySessionImageCredentialsV2)
	if err != nil || !verified {
		return store.SessionBootstrapEvidence{}, errBootstrapImageStage
	}
	result, err := s.apply(ctx, binding)
	if err != nil || result.UID != 0 || result.OS != "Linux" || result.Hostname != sessionHostname(binding.SessionID, binding.EpochID, binding.Generation) || result.Policy != "session-image-credentials-v2" || !bootstrapDigestPattern.MatchString(result.IsolationSHA256) {
		return store.SessionBootstrapEvidence{}, errBootstrapImageStage
	}
	return bootstrapImageSanitizedEvidence(binding, backup.SHA256, result)
}

func (s *bootstrapImageSanitizedStage) Observe(ctx context.Context, binding store.SessionRuntimeBinding) (store.SessionBootstrapEvidence, bool, error) {
	if s == nil || s.backups == nil || s.observe == nil || ctx == nil || !validMaterialBinding(binding) {
		return store.SessionBootstrapEvidence{}, false, errBootstrapImageStage
	}
	backup, verified, err := s.backups.observePolicy(ctx, binding, bootstrapArchivePolicySessionImageCredentialsV2)
	if err != nil || !verified {
		return store.SessionBootstrapEvidence{}, false, errBootstrapImageStage
	}
	result, err := s.observe(ctx, binding)
	if err != nil || result.UID != 0 || result.OS != "Linux" || result.Hostname != sessionHostname(binding.SessionID, binding.EpochID, binding.Generation) || result.Policy != "session-image-credentials-v2" || !bootstrapDigestPattern.MatchString(result.IsolationSHA256) {
		return store.SessionBootstrapEvidence{}, false, errBootstrapImageStage
	}
	evidence, err := bootstrapImageSanitizedEvidence(binding, backup.SHA256, result)
	return evidence, err == nil, err
}

func bootstrapImageSanitizedEvidence(binding store.SessionRuntimeBinding, backupSHA256 string, result bootstrapImageSanitationResult) (store.SessionBootstrapEvidence, error) {
	if !validMaterialBinding(binding) || !bootstrapDigestPattern.MatchString(backupSHA256) || result.UID != 0 || result.OS != "Linux" || result.Hostname != sessionHostname(binding.SessionID, binding.EpochID, binding.Generation) || result.Policy != "session-image-credentials-v2" || !bootstrapDigestPattern.MatchString(result.IsolationSHA256) {
		return store.SessionBootstrapEvidence{}, errBootstrapImageStage
	}
	data, err := json.Marshal(struct {
		Binding          store.SessionRuntimeBinding `json:"binding"`
		Policy           string                      `json:"policy"`
		BackupSHA256     string                      `json:"backup_sha256"`
		IsolationSHA256  string                      `json:"isolation_sha256"`
		InstallerVersion int                         `json:"installer_version"`
	}{materialBinding(binding), result.Policy, backupSHA256, result.IsolationSHA256, 1})
	if err != nil {
		return store.SessionBootstrapEvidence{}, errBootstrapImageStage
	}
	digest := sha256.Sum256(data)
	return store.SessionBootstrapEvidence{SHA256: hex.EncodeToString(digest[:])}, nil
}
