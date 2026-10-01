package sessionruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/WilliamLi0623/codex-homelab/internal/store"
)

var errBootstrapHostPin = errors.New("bootstrap host pin could not be verified")

// bootstrapHostPinStage is a single-stage adapter. Its caller must invoke
// Apply only after obtaining a fresh durable host-pin claim and preparing the
// generation-specific SSH material. This adapter never prepares material.
type bootstrapHostPinStage struct {
	runtime  *ProxmoxRuntime
	registry *SSHMaterialRegistry
}

func newBootstrapHostPinStage(runtime *ProxmoxRuntime, registry *SSHMaterialRegistry) (*bootstrapHostPinStage, error) {
	if runtime == nil || registry == nil {
		return nil, ErrBootstrapConfiguration
	}
	return &bootstrapHostPinStage{runtime: runtime, registry: registry}, nil
}

// Apply must be called only for a fresh durable host-pin claim. The console
// probe is read-only and fenced; Pin is exclusive and cannot rotate a pin.
func (s *bootstrapHostPinStage) Apply(ctx context.Context, binding store.SessionRuntimeBinding) (store.SessionBootstrapEvidence, error) {
	if s == nil || s.runtime == nil || s.registry == nil || !validMaterialBinding(binding) {
		return store.SessionBootstrapEvidence{}, ErrBootstrapBinding
	}
	if ctx == nil || ctx.Err() != nil {
		return store.SessionBootstrapEvidence{}, ErrConsoleCanceled
	}
	probe, err := s.runtime.ProbeBootstrapHostKey(ctx, binding)
	if err != nil || !validMaterialPublicKey(probe.PublicKey) || !validBootstrapEvidence(store.SessionBootstrapEvidence{SHA256: probe.IsolationSHA256}) {
		return store.SessionBootstrapEvidence{}, errBootstrapHostPin
	}
	if err := ctx.Err(); err != nil {
		return store.SessionBootstrapEvidence{}, ErrConsoleCanceled
	}
	if err := s.registry.Pin(binding, probe.PublicKey); err != nil {
		return store.SessionBootstrapEvidence{}, errBootstrapHostPin
	}
	if ctx.Err() != nil {
		return store.SessionBootstrapEvidence{}, ErrConsoleCanceled
	}
	material, err := s.registry.Load(binding)
	if err != nil || !bootstrapHostPinMatches(material, probe.PublicKey) {
		return store.SessionBootstrapEvidence{}, errBootstrapHostPin
	}
	return bootstrapHostPinEvidence(binding, material.Alias, probe.PublicKey, probe.IsolationSHA256)
}

// Observe reconciles an existing pin against a fresh fenced console
// observation. It is strictly read-only and never repairs or retries a pin.
func (s *bootstrapHostPinStage) Observe(ctx context.Context, binding store.SessionRuntimeBinding) (store.SessionBootstrapEvidence, bool, error) {
	if s == nil || s.runtime == nil || s.registry == nil || !validMaterialBinding(binding) {
		return store.SessionBootstrapEvidence{}, false, ErrBootstrapBinding
	}
	if ctx == nil || ctx.Err() != nil {
		return store.SessionBootstrapEvidence{}, false, ErrConsoleCanceled
	}
	probe, err := s.runtime.ProbeBootstrapHostKey(ctx, binding)
	if err != nil || !validMaterialPublicKey(probe.PublicKey) || !validBootstrapEvidence(store.SessionBootstrapEvidence{SHA256: probe.IsolationSHA256}) {
		return store.SessionBootstrapEvidence{}, false, errBootstrapHostPin
	}
	if ctx.Err() != nil {
		return store.SessionBootstrapEvidence{}, false, ErrConsoleCanceled
	}
	material, err := s.registry.Load(binding)
	if err != nil || !bootstrapHostPinMatches(material, probe.PublicKey) {
		return store.SessionBootstrapEvidence{}, false, errBootstrapHostPin
	}
	evidence, err := bootstrapHostPinEvidence(binding, material.Alias, probe.PublicKey, probe.IsolationSHA256)
	if err != nil {
		return store.SessionBootstrapEvidence{}, false, errBootstrapHostPin
	}
	return evidence, true, nil
}

func bootstrapHostPinMatches(material SSHMaterial, observedPublicKey string) bool {
	if material.Alias == "" || !validMaterialPublicKey(observedPublicKey) {
		return false
	}
	data, err := readMaterialFile(material.KnownHostsFile, 4096)
	return err == nil && string(data) == material.Alias+" "+observedPublicKey+"\n"
}

func bootstrapHostPinEvidence(binding store.SessionRuntimeBinding, alias, publicKey, isolationSHA256 string) (store.SessionBootstrapEvidence, error) {
	if !validMaterialBinding(binding) || alias == "" || !validMaterialPublicKey(publicKey) || !bootstrapDigestPattern.MatchString(isolationSHA256) {
		return store.SessionBootstrapEvidence{}, errBootstrapHostPin
	}
	identity := struct {
		BindingID      string `json:"binding_id"`
		SessionID      string `json:"session_id"`
		EpochID        string `json:"epoch_id"`
		Generation     string `json:"generation"`
		VMID           int    `json:"vmid"`
		MaterialAlias  string `json:"material_alias"`
		ObservedKey    string `json:"observed_host_key"`
		IsolationProof string `json:"isolation_sha256"`
	}{binding.ID, binding.SessionID, binding.EpochID, binding.Generation, binding.VMID, alias, publicKey, isolationSHA256}
	data, err := json.Marshal(identity)
	if err != nil {
		return store.SessionBootstrapEvidence{}, errBootstrapHostPin
	}
	digest := sha256.Sum256(data)
	return store.SessionBootstrapEvidence{SHA256: hex.EncodeToString(digest[:])}, nil
}
