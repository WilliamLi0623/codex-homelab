package capacity

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

var ErrIdentityInvalid = errors.New("dynamic worker identity is invalid")

var identityPart = regexp.MustCompile(`[^a-z0-9-]+`)

// DynamicHostname returns a deterministic, DNS-safe name for a worker. Long
// generation values are bounded because Proxmox/Kubernetes names are limited
// to 63 characters; the generation remains persisted separately for exact
// ownership checks.
func DynamicHostname(vmid int, generation string) (string, error) {
	if vmid < 3000 || vmid > 3999 || strings.TrimSpace(generation) == "" {
		return "", ErrIdentityInvalid
	}
	part := strings.ToLower(identityPart.ReplaceAllString(generation, "-"))
	part = strings.Trim(part, "-")
	if part == "" {
		return "", ErrIdentityInvalid
	}
	const prefix = "codex-lxc-"
	name := fmt.Sprintf("%s%d-%s", prefix, vmid, part)
	if len(name) > 63 {
		name = name[:63]
		name = strings.TrimRight(name, "-")
	}
	return name, nil
}
