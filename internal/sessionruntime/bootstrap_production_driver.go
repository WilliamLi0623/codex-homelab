package sessionruntime

import (
	"context"
	"path"
	"strings"

	"github.com/WilliamLi0623/codex-homelab/internal/store"
)

// BootstrapDriverConfig names existing private Controller paths and trusted
// executables. NewBootstrapDriver only validates/references them; it creates
// no directories and does not enable a Manager or API route.
type BootstrapDriverConfig struct {
	SSHMaterialRoot    string
	SSHKeygen          string
	BackupRoot         string
	ArtifactRoot       string
	HelperArtifactPath string
	HelperSHA256       string
	CodexVersion       string
	SSHExecutable      string
}

// NewBootstrapDriver assembles all ordered source adapters around one
// ProxmoxRuntime. Production callers must still explicitly attach the result
// to a BootstrapCoordinator and keep the API/READY release gates closed.
func NewBootstrapDriver(runtime *ProxmoxRuntime, config BootstrapDriverConfig) (BootstrapDriver, error) {
	if runtime == nil || !validBootstrapPOSIXPath(config.HelperArtifactPath) || !bootstrapDigestPattern.MatchString(config.HelperSHA256) || !validBootstrapPOSIXPath(config.SSHMaterialRoot) || !validBootstrapPOSIXPath(config.SSHKeygen) || !validBootstrapPOSIXPath(config.BackupRoot) || !validBootstrapPOSIXPath(config.ArtifactRoot) || !validBootstrapPOSIXPath(config.SSHExecutable) {
		return nil, ErrBootstrapConfiguration
	}
	if _, err := bootstrapCodexBundleForVersion(config.CodexVersion); err != nil {
		return nil, ErrBootstrapConfiguration
	}
	material, err := NewSSHMaterialRegistry(config.SSHMaterialRoot, config.SSHKeygen)
	if err != nil {
		return nil, ErrBootstrapConfiguration
	}
	backups, err := newBootstrapBackupStore(config.BackupRoot)
	if err != nil {
		return nil, ErrBootstrapConfiguration
	}
	resolver, err := newBootstrapAddressSSHConfigResolver(runtime, config.SSHExecutable)
	if err != nil {
		return nil, ErrBootstrapConfiguration
	}
	guestIdentity, err := newBootstrapGuestIdentityStage(runtime, material)
	if err != nil {
		return nil, ErrBootstrapConfiguration
	}
	hostPin, err := newBootstrapHostPinStage(runtime, material)
	if err != nil {
		return nil, ErrBootstrapConfiguration
	}
	imageBackup, err := newBootstrapImageBackupStage(runtime, backups)
	if err != nil {
		return nil, ErrBootstrapConfiguration
	}
	imageSanitized, err := newBootstrapImageSanitizedStage(runtime, backups)
	if err != nil {
		return nil, ErrBootstrapConfiguration
	}
	network, err := newBootstrapNetworkStage(runtime)
	if err != nil {
		return nil, ErrBootstrapConfiguration
	}
	artifact, err := newBootstrapArtifactStage(config.ArtifactRoot, config.CodexVersion, material, resolver)
	if err != nil {
		return nil, ErrBootstrapConfiguration
	}
	helper, err := newBootstrapHelperStage(config.HelperArtifactPath, config.HelperSHA256, material, resolver)
	if err != nil {
		return nil, ErrBootstrapConfiguration
	}
	transport, err := newBootstrapTransportStage(material, resolver)
	if err != nil {
		return nil, ErrBootstrapConfiguration
	}
	stages := map[store.SessionBootstrapStage]bootstrapBoundStage{
		store.SessionBootstrapIsolation:         &bootstrapIsolationStage{runtime: runtime},
		store.SessionBootstrapGuestIdentity:     guestIdentity,
		store.SessionBootstrapHostPin:           hostPin,
		store.SessionBootstrapImageBackup:       imageBackup,
		store.SessionBootstrapImageSanitized:    imageSanitized,
		store.SessionBootstrapNetworkEnabled:    network,
		store.SessionBootstrapHelperVerified:    helper,
		store.SessionBootstrapArtifactVerified:  artifact,
		store.SessionBootstrapTransportVerified: transport,
	}
	return newBootstrapStageDriver(stages, runtime.VerifyIdentity)
}

func validBootstrapPOSIXPath(value string) bool {
	return value != "" && path.IsAbs(value) && path.Clean(value) == value && !strings.ContainsAny(value, "\x00\r\n")
}

type bootstrapIsolationStage struct{ runtime *ProxmoxRuntime }

func (s *bootstrapIsolationStage) Apply(ctx context.Context, binding store.SessionRuntimeBinding) (store.SessionBootstrapEvidence, error) {
	if s == nil || s.runtime == nil {
		return store.SessionBootstrapEvidence{}, ErrBootstrapConfiguration
	}
	return s.runtime.VerifyBootstrapIsolation(ctx, binding)
}

func (s *bootstrapIsolationStage) Observe(ctx context.Context, binding store.SessionRuntimeBinding) (store.SessionBootstrapEvidence, bool, error) {
	evidence, err := s.Apply(ctx, binding)
	return evidence, err == nil, err
}
