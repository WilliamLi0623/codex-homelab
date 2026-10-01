package sessionruntime

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/WilliamLi0623/codex-homelab/internal/store"
)

const (
	bootstrapGuestIdentityHostKeyPath          = "/etc/ssh/ssh_host_p28_ed25519_key"
	bootstrapGuestIdentityHostPublicKeyPath    = "/etc/ssh/ssh_host_p28_ed25519_key.pub"
	bootstrapGuestIdentitySSHDirectory         = "/etc/ssh"
	bootstrapGuestIdentityDropinDirectory      = "/etc/ssh/sshd_config.d"
	bootstrapGuestIdentityDropinPath           = "/etc/ssh/sshd_config.d/00-p28-session.conf"
	bootstrapGuestIdentitySessionDirectory     = "/etc/codex-session"
	bootstrapGuestIdentityMarkerPath           = "/etc/codex-session/bootstrap-identity.json"
	bootstrapGuestIdentityRootSSHDirectory     = "/root/.ssh"
	bootstrapGuestIdentityAuthorizedKeysPath   = "/root/.ssh/authorized_keys"
	bootstrapGuestIdentityAuthorizedKeysParent = "/root"
)

var errBootstrapGuestIdentityCommand = errors.New("bootstrap guest identity command is invalid")

// makeBootstrapGuestIdentityCommand creates a fixed installer for a Linux
// guest. The caller must hold a fresh durable identity-stage claim before
// running the command. It does not claim or complete any bootstrap stage.
//
// Binding and public-key data are carried only in a base64 JSON payload; no
// caller value is interpolated into shell syntax. The command fails closed on
// any pre-existing identity artifact or incomplete installation.
func makeBootstrapGuestIdentityCommand(binding store.SessionRuntimeBinding, clientPublicKey string) ([6]string, string, error) {
	var markers [6]string
	if !validMaterialBinding(binding) || !validMaterialPublicKey(clientPublicKey) {
		return markers, "", errBootstrapGuestIdentityCommand
	}
	marker, err := bootstrapGuestIdentityMarkerData(binding, clientPublicKey)
	if err != nil {
		return markers, "", errBootstrapGuestIdentityCommand
	}
	payload := base64.StdEncoding.EncodeToString(marker)
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return markers, "", errBootstrapGuestIdentityCommand
	}
	base := "__CODEX_GUESTID_" + hex.EncodeToString(nonce)
	markers = [6]string{base + "_BEGIN__", base + "_UID__", base + "_OS__", base + "_HOST__", base + "_KEY__", base + "_END__"}

	// The script receives only base64 data. All executable fragments, paths,
	// tools and actions are fixed in this function.
	script := "set -eu\n" +
		"umask 077\n" +
		"set -C\n" +
		"fail() { printf '%s\\n' 'bootstrap guest identity failed' >&2; exit 1; }\n" +
		"PAYLOAD_B64='" + payload + "'\n" +
		"CLIENT_KEY_B64='" + base64.StdEncoding.EncodeToString([]byte(clientPublicKey)) + "'\n" +
		"CLIENT_KEY=$(printf '%s' \"$CLIENT_KEY_B64\" | base64 -d) || fail\n" +
		"[ \"$(id -u 2>/dev/null)\" = 0 ] || fail\n" +
		"[ \"$(uname -s 2>/dev/null)\" = Linux ] || fail\n" +
		"[ \"$(hostname 2>/dev/null)\" = '" + sessionHostname(binding.SessionID, binding.EpochID, binding.Generation) + "' ] || fail\n" +
		"printf '%s\\n' '" + markers[0] + "'\n" +
		"printf '%s\\n' '" + markers[1] + "'; id -u\n" +
		"printf '%s\\n' '" + markers[2] + "'; uname -s\n" +
		"printf '%s\\n' '" + markers[3] + "'; hostname\n" +
		"check_dir() { [ -d \"$1\" ] && [ ! -L \"$1\" ] || fail; mount=$(findmnt --noheadings --output TARGET --target \"$1\" 2>/dev/null) || fail; [ \"$mount\" = / ] || fail; owner=$(stat -c %u -- \"$1\" 2>/dev/null) || fail; [ \"$owner\" = 0 ] || fail; unsafe=$(find \"$1\" -maxdepth 0 -perm /022 -print 2>/dev/null) || fail; [ -z \"$unsafe\" ] || fail; }\n" +
		"for path in /etc /etc/ssh /etc/ssh/sshd_config.d /root; do check_dir \"$path\"; done\n" +
		"for path in /etc/codex-session /root/.ssh; do if [ -e \"$path\" ] || [ -L \"$path\" ]; then check_dir \"$path\"; fi; done\n" +
		"[ -d /etc/ssh ] && [ -d /etc/ssh/sshd_config.d ] || fail\n" +
		"[ -d /root ] || fail\n" +
		"for path in " + bootstrapGuestIdentityHostKeyPath + " " + bootstrapGuestIdentityHostPublicKeyPath + " " + bootstrapGuestIdentityDropinPath + " " + bootstrapGuestIdentityMarkerPath + "; do [ ! -e \"$path\" ] && [ ! -L \"$path\" ] || fail; done\n" +
		"[ ! -L " + bootstrapGuestIdentityAuthorizedKeysPath + " ] || fail\n" +
		"if [ -e " + bootstrapGuestIdentityAuthorizedKeysPath + " ]; then [ -f " + bootstrapGuestIdentityAuthorizedKeysPath + " ] || fail; metadata=$(stat -c '%u %h' -- " + bootstrapGuestIdentityAuthorizedKeysPath + " 2>/dev/null) || fail; set -- $metadata; [ \"$#\" = 2 ] && [ \"$1\" = 0 ] && [ \"$2\" = 1 ] || fail; unsafe=$(find " + bootstrapGuestIdentityAuthorizedKeysPath + " -maxdepth 0 -perm /022 -print 2>/dev/null) || fail; [ -z \"$unsafe\" ] || fail; fi\n" +
		"for path in " + bootstrapGuestIdentityHostKeyPath + " " + bootstrapGuestIdentityHostPublicKeyPath + " " + bootstrapGuestIdentityDropinPath + " " + bootstrapGuestIdentityMarkerPath + " " + bootstrapGuestIdentityAuthorizedKeysPath + "; do if [ -e \"$path\" ] || [ -L \"$path\" ]; then mount=$(findmnt --noheadings --output TARGET --target \"$path\" 2>/dev/null) || fail; [ \"$mount\" = / ] || fail; fi; done\n" +
		"[ -d /etc/codex-session ] || mkdir -m 700 /etc/codex-session || fail\n" +
		"[ -d /root/.ssh ] || mkdir -m 700 /root/.ssh || fail\n" +
		"if [ ! -e " + bootstrapGuestIdentityAuthorizedKeysPath + " ]; then : > " + bootstrapGuestIdentityAuthorizedKeysPath + " || fail; fi\n" +
		"chmod 700 /root/.ssh 2>/dev/null || fail; chmod 600 " + bootstrapGuestIdentityAuthorizedKeysPath + " 2>/dev/null || fail\n" +
		"if grep -F -x -q -- \"$CLIENT_KEY\" " + bootstrapGuestIdentityAuthorizedKeysPath + " 2>/dev/null; then fail; else grep_status=$?; [ \"$grep_status\" = 1 ] || fail; fi\n" +
		"ssh-keygen -q -t ed25519 -N '' -f " + bootstrapGuestIdentityHostKeyPath + " -C p28-session-host 2>/dev/null || fail\n" +
		"[ -f " + bootstrapGuestIdentityHostKeyPath + " ] && [ ! -L " + bootstrapGuestIdentityHostKeyPath + " ] && [ -f " + bootstrapGuestIdentityHostPublicKeyPath + " ] && [ ! -L " + bootstrapGuestIdentityHostPublicKeyPath + " ] || fail\n" +
		"chmod 600 " + bootstrapGuestIdentityHostKeyPath + " " + bootstrapGuestIdentityHostPublicKeyPath + " 2>/dev/null || fail\n" +
		"derived_full=$(ssh-keygen -y -f " + bootstrapGuestIdentityHostKeyPath + " 2>/dev/null) || fail\n" +
		"derived=$(printf '%s\\n' \"$derived_full\" | awk 'NF >= 2 {print $1 \" \" $2}') || fail\n" +
		"stored=$(awk 'NF >= 2 {print $1 \" \" $2}' " + bootstrapGuestIdentityHostPublicKeyPath + " 2>/dev/null) || fail\n" +
		"[ -n \"$derived\" ] && [ \"$derived\" = \"$stored\" ] || fail\n" +
		"printf '%s\\n' '" + markers[4] + "'; cat -- " + bootstrapGuestIdentityHostPublicKeyPath + "\n" +
		"printf '%s\\n' 'HostKey " + bootstrapGuestIdentityHostKeyPath + "' 'PasswordAuthentication no' 'KbdInteractiveAuthentication no' 'PubkeyAuthentication yes' > " + bootstrapGuestIdentityDropinPath + " 2>/dev/null || fail\n" +
		"chmod 600 " + bootstrapGuestIdentityDropinPath + " 2>/dev/null || fail\n" +
		"if [ -s " + bootstrapGuestIdentityAuthorizedKeysPath + " ]; then last_newline=$(tail -c 1 " + bootstrapGuestIdentityAuthorizedKeysPath + " | wc -l) || fail; [ \"$last_newline\" -gt 0 ] || printf '\\n' >> " + bootstrapGuestIdentityAuthorizedKeysPath + " || fail; fi\n" +
		"printf '%s\\n' \"$CLIENT_KEY\" >> " + bootstrapGuestIdentityAuthorizedKeysPath + " || fail\n" +
		"sshd -t 2>/dev/null || fail\n" +
		"effective=$(sshd -T 2>/dev/null) || fail\n" +
		"printf '%s\\n' \"$effective\" | awk 'BEGIN {hostkeys=0; ok=1} $1==\"hostkey\" {hostkeys++; if ($2!=\"" + bootstrapGuestIdentityHostKeyPath + "\") ok=0} $1==\"passwordauthentication\" && $2==\"no\" {password=1} $1==\"kbdinteractiveauthentication\" && $2==\"no\" {keyboard=1} $1==\"pubkeyauthentication\" && $2==\"yes\" {pubkey=1} END {if (hostkeys!=1 || !password || !keyboard || !pubkey || !ok) exit 1}' || fail\n" +
		"printf '%s' \"$PAYLOAD_B64\" | base64 -d > " + bootstrapGuestIdentityMarkerPath + " 2>/dev/null || fail\n" +
		"chmod 600 " + bootstrapGuestIdentityMarkerPath + " 2>/dev/null || fail\n" +
		"sync -f " + bootstrapGuestIdentityMarkerPath + " 2>/dev/null || fail\n" +
		"sync -f /etc/codex-session 2>/dev/null || fail\n" +
		"printf '%s\\n' '" + markers[5] + "'\n"
	encoded := base64.StdEncoding.EncodeToString([]byte(script))
	return markers, "printf %s '" + encoded + "' | base64 -d | sh\n", nil
}

func validGuestIdentityMarker(marker string) bool {
	if !strings.HasPrefix(marker, "__CODEX_GUESTID_") || !strings.HasSuffix(marker, "__") {
		return false
	}
	body := strings.TrimSuffix(strings.TrimPrefix(marker, "__CODEX_GUESTID_"), "__")
	if len(body) < 33 {
		return false
	}
	_, err := hex.DecodeString(body[:32])
	if err != nil {
		return false
	}
	switch body[32:] {
	case "_BEGIN", "_UID", "_OS", "_HOST", "_KEY", "_END":
		return true
	default:
		return false
	}
}
