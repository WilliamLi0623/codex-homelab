package sessionruntime

import (
	"context"
	"path"
	"strings"

	"github.com/WilliamLi0623/codex-homelab/internal/codexsession"
	"github.com/WilliamLi0623/codex-homelab/internal/store"
)

// newBootstrapAddressSSHConfigResolver bridges the ephemeral guest address
// observation into the existing generation-pinned SSH transport. It never
// stores the address; every resolution re-probes the exact owned guest.
func newBootstrapAddressSSHConfigResolver(runtime *ProxmoxRuntime, sshExecutable string) (bootstrapArtifactSSHConfigResolver, error) {
	if runtime == nil || !path.IsAbs(sshExecutable) || path.Clean(sshExecutable) != sshExecutable || strings.ContainsAny(sshExecutable, "\x00\r\n") {
		return nil, ErrBootstrapConfiguration
	}
	return func(ctx context.Context, binding store.SessionRuntimeBinding, material SSHMaterial) (codexsession.SSHAppServerConfig, error) {
		var empty codexsession.SSHAppServerConfig
		if ctx == nil || !validMaterialBinding(binding) {
			return empty, ErrBootstrapGuestAddress
		}
		probe, err := runtime.ProbeBootstrapGuestIPv4(ctx, binding)
		if err != nil {
			return empty, ErrBootstrapGuestAddress
		}
		home := bootstrapCodexHome(binding)
		if home == "" {
			return empty, ErrBootstrapGuestAddress
		}
		return codexsession.SSHAppServerConfig{
			SSHExecutable:  sshExecutable,
			Address:        probe.Address.String(),
			IdentityFile:   material.IdentityFile,
			KnownHostsFile: material.KnownHostsFile,
			HostKeyAlias:   material.Alias,
			CodexHome:      home,
		}, nil
	}, nil
}

func bootstrapCodexHome(binding store.SessionRuntimeBinding) string {
	if !validMaterialBinding(binding) {
		return ""
	}
	return "/root/.codex-p28/" + materialDigest([]byte(binding.ID+"\x00"+binding.SessionID+"\x00"+binding.EpochID+"\x00"+binding.Generation))
}
