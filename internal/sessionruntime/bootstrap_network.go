package sessionruntime

import (
	"encoding/json"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

var pveConfigurationDigest = regexp.MustCompile(`^[0-9a-f]{40}$`)

// bootstrapNetworkEnableForm constructs only a compare-and-set network patch.
// It performs no requests and grants no mutation authority. The production
// driver must first verify the current binding, completed sanitation/host-pin
// checkpoints, and the fenced snapshot; after PUT it must verify readback.
func bootstrapNetworkEnableForm(config map[string]json.RawMessage) (url.Values, error) {
	var digest string
	if json.Unmarshal(config["digest"], &digest) != nil || !pveConfigurationDigest.MatchString(digest) {
		return nil, ErrRuntimeNotReady
	}
	form := url.Values{"digest": {digest}}
	for key, raw := range config {
		if !strings.HasPrefix(key, "net") {
			continue
		}
		if _, err := strconv.ParseUint(strings.TrimPrefix(key, "net"), 10, 8); err != nil {
			return nil, ErrRuntimeNotReady
		}
		var network string
		if json.Unmarshal(raw, &network) != nil {
			return nil, ErrRuntimeNotReady
		}
		parts := strings.Split(network, ",")
		options := make(map[string]string, len(parts))
		for index, part := range parts {
			name, value, ok := strings.Cut(part, "=")
			if !ok || name == "" || value == "" {
				return nil, ErrRuntimeNotReady
			}
			if _, duplicate := options[name]; duplicate {
				return nil, ErrRuntimeNotReady
			}
			options[name] = value
			if name == "link_down" {
				parts[index] = "link_down=0"
			}
		}
		if options["link_down"] != "1" || options["ip"] != "dhcp" {
			return nil, ErrRuntimeNotReady
		}
		form.Set(key, strings.Join(parts, ","))
	}
	if len(form) == 1 {
		return nil, ErrRuntimeNotReady
	}
	return form, nil
}
