package sessionruntime

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/WilliamLi0623/codex-homelab/internal/store"
)

var errBootstrapImageSanitizeCommand = errors.New("bootstrap image sanitation command is invalid")

func makeBootstrapImageSanitizeCommand(binding store.SessionRuntimeBinding, policy bootstrapArchivePolicy) ([6]string, string, error) {
	return makeBootstrapImageSanitationCommand(binding, policy, true)
}

func makeBootstrapImageSanitationObserverCommand(binding store.SessionRuntimeBinding, policy bootstrapArchivePolicy) ([6]string, string, error) {
	return makeBootstrapImageSanitationCommand(binding, policy, false)
}

func makeBootstrapImageSanitationCommand(binding store.SessionRuntimeBinding, policy bootstrapArchivePolicy, mutate bool) ([6]string, string, error) {
	var markers [6]string
	if !validMaterialBinding(binding) || policy != bootstrapArchivePolicySessionImageCredentialsV2 {
		return markers, "", errBootstrapImageSanitizeCommand
	}
	roots, ok := bootstrapArchivePolicyRoots(policy)
	if !ok || len(roots) != 23 {
		return markers, "", errBootstrapImageSanitizeCommand
	}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return markers, "", errBootstrapImageSanitizeCommand
	}
	base := "__CODEX_IMAGESAN_" + hex.EncodeToString(nonce)
	markers = [6]string{base + "_UID__", base + "_OS__", base + "_HOST__", base + "_PREFLIGHT__", base + "_RESULT__", base + "_END__"}
	var script strings.Builder
	script.WriteString("set -eu\numask 077\n")
	script.WriteString("fail() { printf '%s\\n' 'bootstrap image sanitation failed' >&2; exit 1; }\n")
	script.WriteString("printf '%s\\n' '")
	script.WriteString(markers[0])
	script.WriteString("'; id -u\nprintf '%s\\n' '")
	script.WriteString(markers[1])
	script.WriteString("'; uname -s\nprintf '%s\\n' '")
	script.WriteString(markers[2])
	script.WriteString("'; hostname\n")
	script.WriteString("[ \"$(id -u 2>/dev/null)\" = 0 ] || fail\n[ \"$(uname -s 2>/dev/null)\" = Linux ] || fail\n[ \"$(hostname 2>/dev/null)\" = '")
	script.WriteString(sessionHostname(binding.SessionID, binding.EpochID, binding.Generation))
	script.WriteString("' ] || fail\n")
	script.WriteString("root_device=$(stat -c %d / 2>/dev/null) || fail\ncommand -v awk >/dev/null 2>&1 || fail\n")
	for _, root := range roots {
		guestPath := "/" + root
		script.WriteString("target=\"")
		script.WriteString(guestPath)
		script.WriteString("\"\n")
		if mutate {
			script.WriteString("[ -f \"$target\" ] || fail\n[ ! -L \"$target\" ] || fail\n")
			script.WriteString("[ \"$(stat -c %d -- \"$target\" 2>/dev/null)\" = \"$root_device\" ] || fail\n")
		} else {
			script.WriteString("[ ! -e \"$target\" ] && [ ! -L \"$target\" ] || fail\n")
		}
		script.WriteString("mount_match=$(awk -v target=\"$target\" '$5 == target { print \"mounted\" }' /proc/self/mountinfo 2>/dev/null) || fail\n[ -z \"$mount_match\" ] || fail\n")
	}
	script.WriteString("printf '%s\\n' '")
	script.WriteString(markers[3])
	script.WriteString("'\n")
	if mutate {
		script.WriteString("rm --")
		for _, root := range roots {
			script.WriteString(" /")
			script.WriteString(root)
		}
		script.WriteString("\nsync -f / 2>/dev/null || fail\n")
	}
	for _, root := range roots {
		guestPath := "/" + root
		script.WriteString("[ ! -e \"")
		script.WriteString(guestPath)
		script.WriteString("\" ] && [ ! -L \"")
		script.WriteString(guestPath)
		script.WriteString("\" ] || fail\n")
	}
	script.WriteString("printf '%s\\n' '")
	script.WriteString(markers[4])
	script.WriteString("'; printf '%s\\n' 'absent=23'\nprintf '%s\\n' '")
	script.WriteString(markers[5])
	script.WriteString("'\n")
	encoded := base64.StdEncoding.EncodeToString([]byte(script.String()))
	return markers, "decoded=$(printf %s '" + encoded + "' | base64 -d) || exit 1\nexec bash -o pipefail -c \"$decoded\"\n", nil
}
