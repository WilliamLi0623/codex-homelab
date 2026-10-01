package sessionruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/WilliamLi0623/codex-homelab/internal/store"
)

var errBootstrapGuestIdentityStage = errors.New("bootstrap guest identity stage could not be verified")

// bootstrapGuestIdentityStage adapts the local generation-specific SSH
// material and fixed Proxmox console operation to one coordinator stage.
// The coordinator must establish the durable fresh claim before Apply; this
// adapter never claims or completes a Store checkpoint.
type bootstrapGuestIdentityStage struct {
	runtime  *ProxmoxRuntime
	material *SSHMaterialRegistry
}

func newBootstrapGuestIdentityStage(runtime *ProxmoxRuntime, material *SSHMaterialRegistry) (*bootstrapGuestIdentityStage, error) {
	if runtime == nil || material == nil {
		return nil, ErrBootstrapConfiguration
	}
	return &bootstrapGuestIdentityStage{runtime: runtime, material: material}, nil
}

func (s *bootstrapGuestIdentityStage) Apply(ctx context.Context, binding store.SessionRuntimeBinding) (store.SessionBootstrapEvidence, error) {
	if s == nil || s.runtime == nil || s.material == nil || ctx == nil || !validMaterialBinding(binding) {
		return store.SessionBootstrapEvidence{}, errBootstrapGuestIdentityStage
	}
	material, err := s.material.Prepare(ctx, binding)
	if err != nil || !validMaterialPublicKey(material.ClientPublicKey) {
		return store.SessionBootstrapEvidence{}, errBootstrapGuestIdentityStage
	}
	observed, err := s.runtime.InstallBootstrapGuestIdentity(ctx, binding, material.ClientPublicKey)
	if err != nil || observed.UID != 0 || observed.OS != "Linux" || observed.Hostname != sessionHostname(binding.SessionID, binding.EpochID, binding.Generation) {
		return store.SessionBootstrapEvidence{}, errBootstrapGuestIdentityStage
	}
	evidence, err := bootstrapGuestIdentityEvidence(binding, material.ClientPublicKey, observed.IsolationSHA256)
	if err != nil {
		return store.SessionBootstrapEvidence{}, errBootstrapGuestIdentityStage
	}
	return evidence, nil
}

// Observe only reads controller-side material and the guest's fixed identity
// marker. It never creates keys or repairs an incomplete installation.
func (s *bootstrapGuestIdentityStage) Observe(ctx context.Context, binding store.SessionRuntimeBinding) (store.SessionBootstrapEvidence, bool, error) {
	if s == nil || s.runtime == nil || s.material == nil || ctx == nil || !validMaterialBinding(binding) {
		return store.SessionBootstrapEvidence{}, false, errBootstrapGuestIdentityStage
	}
	material, err := s.material.readPrepared(binding)
	if err != nil || !validMaterialPublicKey(material.ClientPublicKey) {
		return store.SessionBootstrapEvidence{}, false, errBootstrapGuestIdentityStage
	}
	observed, err := s.runtime.ObserveBootstrapGuestIdentity(ctx, binding, material.ClientPublicKey)
	if err != nil || observed.UID != 0 || observed.OS != "Linux" || observed.Hostname != sessionHostname(binding.SessionID, binding.EpochID, binding.Generation) {
		return store.SessionBootstrapEvidence{}, false, errBootstrapGuestIdentityStage
	}
	evidence, err := bootstrapGuestIdentityEvidence(binding, material.ClientPublicKey, observed.IsolationSHA256)
	if err != nil {
		return store.SessionBootstrapEvidence{}, false, errBootstrapGuestIdentityStage
	}
	return evidence, true, nil
}

func bootstrapGuestIdentityEvidence(binding store.SessionRuntimeBinding, clientPublicKey, isolationSHA256 string) (store.SessionBootstrapEvidence, error) {
	if !validMaterialBinding(binding) || !validMaterialPublicKey(clientPublicKey) || !bootstrapDigestPattern.MatchString(isolationSHA256) {
		return store.SessionBootstrapEvidence{}, errBootstrapGuestIdentityStage
	}
	identity := struct {
		Binding          store.SessionRuntimeBinding `json:"binding"`
		ClientKeySHA256  string                      `json:"client_key_sha256"`
		IsolationSHA256  string                      `json:"isolation_sha256"`
		InstallerVersion int                         `json:"installer_version"`
	}{materialBinding(binding), materialDigest([]byte(clientPublicKey)), isolationSHA256, 1}
	data, err := json.Marshal(identity)
	if err != nil {
		return store.SessionBootstrapEvidence{}, errBootstrapGuestIdentityStage
	}
	digest := sha256.Sum256(data)
	return store.SessionBootstrapEvidence{SHA256: hex.EncodeToString(digest[:])}, nil
}
