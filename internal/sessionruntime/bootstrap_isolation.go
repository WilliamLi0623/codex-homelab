package sessionruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/WilliamLi0623/codex-homelab/internal/store"
)

// VerifyBootstrapIsolation proves the owned guest's configuration is fenced
// using one read-only PVE snapshot. It neither authorizes a later mutation nor
// proves host-side packet isolation; the console driver must recheck it before
// each pre-network action. Only a digest of non-secret fields is returned.
func (r *ProxmoxRuntime) VerifyBootstrapIsolation(ctx context.Context, binding store.SessionRuntimeBinding) (store.SessionBootstrapEvidence, error) {
	var empty store.SessionBootstrapEvidence
	if r == nil || ctx == nil || !validSessionVMID(binding.VMID) || binding.SessionID == "" || binding.EpochID == "" || binding.Generation == "" {
		return empty, ErrBootstrapBinding
	}
	var envelope struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	path := "/nodes/" + url.PathEscape(r.node) + "/lxc/" + strconv.Itoa(binding.VMID) + "/config"
	if err := r.request(ctx, http.MethodGet, path, nil, &envelope); err != nil {
		return empty, ErrOutcomeUnknown
	}
	config := envelope.Data
	text := func(key string) string { var value string; _ = json.Unmarshal(config[key], &value); return value }
	var owner sessionOwnership
	if decodeOwnership(text("description"), &owner) != nil || owner.Version != 1 || owner.SessionID != binding.SessionID || owner.EpochID != binding.EpochID || owner.Generation != binding.Generation || text("hostname") != sessionHostname(binding.SessionID, binding.EpochID, binding.Generation) {
		return empty, ErrBootstrapBinding
	}
	for _, key := range []string{"vmid", "unprivileged", "onboot"} {
		var value int
		raw, exists := config[key]
		if !exists {
			continue
		} // PVE omits default-zero properties and vmid.
		if json.Unmarshal(raw, &value) != nil || (key == "vmid" && value != binding.VMID) || (key != "vmid" && value != 0) {
			return empty, ErrRuntimeNotReady
		}
	}
	if text("cmode") != "shell" || !strings.HasPrefix(volumeIDFromMount(text("rootfs")), "local:") || !strings.HasPrefix(volumeIDFromMount(text("mp0")), "pool:") || !hasMountPoint(text("mp0"), "/workspace") {
		return empty, ErrRuntimeNotReady
	}
	projection := map[string]string{"hostname": text("hostname"), "description": text("description"), "rootfs": text("rootfs"), "mp0": text("mp0"), "cmode": "shell", "vmid": strconv.Itoa(binding.VMID), "onboot": "0", "unprivileged": "0"}
	networks := 0
	for key := range config {
		if strings.HasPrefix(key, "mp") && key != "mp0" {
			return empty, ErrRuntimeNotReady
		}
		if !strings.HasPrefix(key, "net") {
			continue
		}
		index := strings.TrimPrefix(key, "net")
		if _, err := strconv.ParseUint(index, 10, 8); err != nil {
			return empty, ErrRuntimeNotReady
		}
		options := make(map[string]string)
		for _, option := range strings.Split(text(key), ",") {
			name, value, ok := strings.Cut(option, "=")
			if !ok || name == "" || value == "" {
				return empty, ErrRuntimeNotReady
			}
			if _, duplicate := options[name]; duplicate {
				return empty, ErrRuntimeNotReady
			}
			options[name] = value
		}
		if options["link_down"] != "1" || options["ip"] != "dhcp" {
			return empty, ErrRuntimeNotReady
		}
		projection[key] = text(key)
		networks++
	}
	if networks == 0 {
		return empty, ErrRuntimeNotReady
	}
	data, err := json.Marshal(projection)
	if err != nil {
		return empty, ErrOutcomeUnknown
	}
	digest := sha256.Sum256(data)
	return store.SessionBootstrapEvidence{SHA256: hex.EncodeToString(digest[:])}, nil
}
