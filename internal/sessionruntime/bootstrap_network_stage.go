package sessionruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/WilliamLi0623/codex-homelab/internal/store"
)

var errBootstrapNetworkStage = errors.New("bootstrap network release could not be verified")

type bootstrapNetworkStage struct{ runtime *ProxmoxRuntime }

func newBootstrapNetworkStage(runtime *ProxmoxRuntime) (*bootstrapNetworkStage, error) {
	if runtime == nil {
		return nil, ErrBootstrapConfiguration
	}
	return &bootstrapNetworkStage{runtime: runtime}, nil
}

func (s *bootstrapNetworkStage) Apply(ctx context.Context, binding store.SessionRuntimeBinding) (store.SessionBootstrapEvidence, error) {
	if s == nil || s.runtime == nil || ctx == nil || !validMaterialBinding(binding) {
		return store.SessionBootstrapEvidence{}, errBootstrapNetworkStage
	}
	if _, err := s.runtime.VerifyBootstrapIsolation(ctx, binding); err != nil {
		return store.SessionBootstrapEvidence{}, errBootstrapNetworkStage
	}
	if err := s.runtime.VerifyIdentity(ctx, binding); err != nil {
		return store.SessionBootstrapEvidence{}, errBootstrapNetworkStage
	}
	config, err := s.runtime.bootstrapNetworkConfig(ctx, binding)
	if err != nil {
		return store.SessionBootstrapEvidence{}, errBootstrapNetworkStage
	}
	form, err := bootstrapNetworkEnableForm(config)
	if err != nil {
		return store.SessionBootstrapEvidence{}, errBootstrapNetworkStage
	}
	path := "/nodes/" + url.PathEscape(s.runtime.node) + "/lxc/" + strconv.Itoa(binding.VMID) + "/config"
	if err := s.runtime.request(ctx, http.MethodPut, path, form, nil); err != nil {
		// A failed/ambiguous PUT is never retried here. Coordinator reconciliation
		// must prove the resulting state through Observe.
		return store.SessionBootstrapEvidence{}, errBootstrapNetworkStage
	}
	evidence, verified, err := s.Observe(ctx, binding)
	if err != nil || !verified {
		return store.SessionBootstrapEvidence{}, errBootstrapNetworkStage
	}
	return evidence, nil
}

func (s *bootstrapNetworkStage) Observe(ctx context.Context, binding store.SessionRuntimeBinding) (store.SessionBootstrapEvidence, bool, error) {
	if s == nil || s.runtime == nil || ctx == nil || !validMaterialBinding(binding) {
		return store.SessionBootstrapEvidence{}, false, errBootstrapNetworkStage
	}
	if err := s.runtime.VerifyIdentity(ctx, binding); err != nil {
		return store.SessionBootstrapEvidence{}, false, errBootstrapNetworkStage
	}
	config, err := s.runtime.bootstrapNetworkConfig(ctx, binding)
	if err != nil {
		return store.SessionBootstrapEvidence{}, false, errBootstrapNetworkStage
	}
	networks, err := bootstrapEnabledNetworkProjection(config)
	if err != nil {
		return store.SessionBootstrapEvidence{}, false, errBootstrapNetworkStage
	}
	evidence, err := bootstrapNetworkEvidence(binding, networks)
	return evidence, err == nil, err
}

func (r *ProxmoxRuntime) bootstrapNetworkConfig(ctx context.Context, binding store.SessionRuntimeBinding) (map[string]json.RawMessage, error) {
	if r == nil || ctx == nil || !validMaterialBinding(binding) {
		return nil, errBootstrapNetworkStage
	}
	var envelope struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	path := "/nodes/" + url.PathEscape(r.node) + "/lxc/" + strconv.Itoa(binding.VMID) + "/config"
	if err := r.request(ctx, http.MethodGet, path, nil, &envelope); err != nil || !bootstrapNetworkSnapshotMatchesBinding(envelope.Data, binding) {
		return nil, errBootstrapNetworkStage
	}
	return envelope.Data, nil
}

func bootstrapNetworkSnapshotMatchesBinding(config map[string]json.RawMessage, binding store.SessionRuntimeBinding) bool {
	if len(config) == 0 || !validMaterialBinding(binding) {
		return false
	}
	text := func(key string) string {
		var value string
		_ = json.Unmarshal(config[key], &value)
		return value
	}
	var owner sessionOwnership
	if decodeOwnership(text("description"), &owner) != nil || owner.Version != 1 || owner.SessionID != binding.SessionID || owner.EpochID != binding.EpochID || owner.Generation != binding.Generation || text("hostname") != sessionHostname(binding.SessionID, binding.EpochID, binding.Generation) || text("cmode") != "shell" || !strings.HasPrefix(volumeIDFromMount(text("rootfs")), SessionSystemStorage+":") || !strings.HasPrefix(volumeIDFromMount(text("mp0")), "pool:") || !hasMountPoint(text("mp0"), "/workspace") {
		return false
	}
	for key, expected := range map[string]int{"vmid": binding.VMID, "unprivileged": 0, "onboot": 0} {
		if raw, exists := config[key]; exists {
			var actual int
			if json.Unmarshal(raw, &actual) != nil || actual != expected {
				return false
			}
		}
	}
	for key := range config {
		if strings.HasPrefix(key, "mp") && key != "mp0" {
			return false
		}
	}
	return true
}

func bootstrapEnabledNetworkProjection(config map[string]json.RawMessage) (map[string]string, error) {
	networks := make(map[string]string)
	for key, raw := range config {
		if !strings.HasPrefix(key, "net") {
			continue
		}
		if _, err := strconv.ParseUint(strings.TrimPrefix(key, "net"), 10, 8); err != nil {
			return nil, errBootstrapNetworkStage
		}
		var network string
		if json.Unmarshal(raw, &network) != nil {
			return nil, errBootstrapNetworkStage
		}
		parts := strings.Split(network, ",")
		options := make(map[string]string, len(parts))
		for _, part := range parts {
			name, value, ok := strings.Cut(part, "=")
			if !ok || name == "" || value == "" {
				return nil, errBootstrapNetworkStage
			}
			if _, duplicate := options[name]; duplicate {
				return nil, errBootstrapNetworkStage
			}
			options[name] = value
		}
		if options["ip"] != "dhcp" || options["link_down"] != "0" {
			return nil, errBootstrapNetworkStage
		}
		networks[key] = network
	}
	if len(networks) != 1 || networks["net0"] == "" {
		return nil, errBootstrapNetworkStage
	}
	options := make(map[string]string)
	for _, part := range strings.Split(networks["net0"], ",") {
		name, value, ok := strings.Cut(part, "=")
		if !ok || name == "" || value == "" {
			return nil, errBootstrapNetworkStage
		}
		if _, duplicate := options[name]; duplicate {
			return nil, errBootstrapNetworkStage
		}
		options[name] = value
	}
	if options["name"] != "eth0" || options["bridge"] != "vmbr0" {
		return nil, errBootstrapNetworkStage
	}
	return networks, nil
}

func bootstrapNetworkEvidence(binding store.SessionRuntimeBinding, networks map[string]string) (store.SessionBootstrapEvidence, error) {
	if !validMaterialBinding(binding) || len(networks) == 0 {
		return store.SessionBootstrapEvidence{}, errBootstrapNetworkStage
	}
	for key := range networks {
		if !strings.HasPrefix(key, "net") {
			return store.SessionBootstrapEvidence{}, errBootstrapNetworkStage
		}
	}
	data, err := json.Marshal(struct {
		Binding  store.SessionRuntimeBinding `json:"binding"`
		Networks map[string]string           `json:"networks"`
	}{materialBinding(binding), networks})
	if err != nil {
		return store.SessionBootstrapEvidence{}, errBootstrapNetworkStage
	}
	digest := sha256.Sum256(data)
	return store.SessionBootstrapEvidence{SHA256: hex.EncodeToString(digest[:])}, nil
}
