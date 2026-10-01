package sessionruntime

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/WilliamLi0623/codex-homelab/internal/store"
)

func TestBootstrapGuestIdentityCommandRejectsInvalidInputs(t *testing.T) {
	binding := store.SessionRuntimeBinding{ID: "binding-a", SessionID: "session-a", EpochID: "epoch-a", Generation: "gen-a", VMID: 4002}
	key := publicMaterialFixture(t)
	for _, tc := range []struct {
		name    string
		binding store.SessionRuntimeBinding
		key     string
	}{
		{name: "missing binding", key: key},
		{name: "binding control character", binding: func() store.SessionRuntimeBinding { b := binding; b.ID = "bad\ncommand"; return b }(), key: key},
		{name: "key with comment", binding: binding, key: key + " caller-value"},
		{name: "authorized keys option", binding: binding, key: "command=touch /tmp/pwn " + key},
		{name: "invalid ed25519 blob", binding: binding, key: "ssh-ed25519 AAAA"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if markers, command, err := makeBootstrapGuestIdentityCommand(tc.binding, tc.key); err == nil || command != "" || markers != ([6]string{}) {
				t.Fatalf("invalid input returned markers=%q command=%q err=%v", markers, command, err)
			}
		})
	}
}

func TestBootstrapGuestIdentityCommandCarriesOnlyFixedPayloadAndHexMarkers(t *testing.T) {
	binding := store.SessionRuntimeBinding{ID: `bind'$(touch /tmp/nope)"`, SessionID: "session-a", EpochID: "epoch-a", Generation: "gen-a", VMID: 4002}
	key := publicMaterialFixture(t)
	markers, command, err := makeBootstrapGuestIdentityCommand(binding, key)
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range markers {
		if !strings.HasPrefix(marker, "__CODEX_GUESTID_") || !strings.HasSuffix(marker, "__") || !validGuestIdentityMarker(marker) {
			t.Fatalf("unsafe marker %q", marker)
		}
	}
	if strings.Contains(command, binding.ID) || strings.Contains(command, key) || strings.Contains(command, "/tmp/nope") {
		t.Fatal("caller binding or public key was interpolated into terminal input")
	}
	scriptBytes := []byte(decodeGuestIdentityScript(t, command))
	var payload string
	line := "PAYLOAD_B64="
	for _, scriptLine := range strings.Split(string(scriptBytes), "\n") {
		if strings.HasPrefix(scriptLine, line) {
			payload = strings.Trim(strings.TrimPrefix(scriptLine, line), "'")
		}
	}
	data, err := base64.StdEncoding.Strict().DecodeString(payload)
	if err != nil {
		t.Fatalf("decode binding payload: %v", err)
	}
	expected, err := bootstrapGuestIdentityMarkerData(binding, key)
	if err != nil || !bytes.Equal(data, expected) || !bootstrapGuestIdentityMarkerMatches(data, binding, key) {
		t.Fatalf("encoded payload differs from the canonical guest identity marker: %s, err=%v", data, err)
	}
	var shape map[string]json.RawMessage
	if err := json.Unmarshal(data, &shape); err != nil || len(shape) != 3 || shape["version"] == nil || shape["binding"] == nil || shape["client_public_key"] == nil {
		t.Fatalf("binding marker JSON has unexpected top-level shape: %s, err=%v", data, err)
	}
	for _, fixed := range []string{bootstrapGuestIdentityHostKeyPath, bootstrapGuestIdentityHostPublicKeyPath, bootstrapGuestIdentityDropinPath, bootstrapGuestIdentityMarkerPath} {
		if !strings.Contains(string(scriptBytes), fixed) {
			t.Errorf("script does not target fixed path %q", fixed)
		}
	}
}

func TestBootstrapGuestIdentityCommandInstallsSafelyInLinuxFixture(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("safe command execution fixture requires Linux shell semantics")
	}
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("safe command execution fixture requires sh")
	}
	binding := store.SessionRuntimeBinding{ID: "binding-fixture", SessionID: "session-a", EpochID: "epoch-a", Generation: "gen-a", VMID: 4002}
	key := publicMaterialFixture(t)
	markers, command, err := makeBootstrapGuestIdentityCommand(binding, key)
	if err != nil {
		t.Fatal(err)
	}
	script := decodeGuestIdentityScript(t, command)
	root := t.TempDir()
	paths := makeGuestIdentityFixturePaths(t, root)
	script = rewriteGuestIdentityFixturePaths(t, script, paths)
	shimDir := filepath.Join(root, "bin")
	if err := os.Mkdir(shimDir, 0700); err != nil {
		t.Fatal(err)
	}
	writeGuestIdentityShim(t, shimDir, "id", "printf '0\\n'\n")
	writeGuestIdentityShim(t, shimDir, "uname", "printf 'Linux\\n'\n")
	writeGuestIdentityShim(t, shimDir, "hostname", "printf '%s\\n' '"+sessionHostname(binding.SessionID, binding.EpochID, binding.Generation)+"'\n")
	writeGuestIdentityShim(t, shimDir, "findmnt", "printf '/\\n'\n")
	writeGuestIdentityShim(t, shimDir, "stat", "if [ \"$1\" = -c ] && [ \"$2\" = %u ]; then printf '0\\n'; elif [ \"$1\" = -c ] && [ \"$2\" = '%u %h' ]; then links=$(/usr/bin/stat -c %h -- \"$4\") || exit 1; printf '0 %s\\n' \"$links\"; else exec /usr/bin/stat \"$@\"; fi\n")
	writeGuestIdentityShim(t, shimDir, "ssh-keygen", "#!/bin/sh\nif [ \"$1\" = -y ]; then printf '%s\\n' '"+key+"'; exit 0; fi\n[ \"$1\" = -q ] || exit 71\nshift 5\n[ \"$1\" = -f ] || exit 72\nkey=$2\n[ ! -e \"$key\" ] && [ ! -e \"$key.pub\" ] || exit 73\nprintf 'fixture-private\\n' > \"$key\"\nprintf '%s\\n' '"+key+"' > \"$key.pub\"\n")
	writeGuestIdentityShim(t, shimDir, "sshd", "case \"$*\" in *-t*) exit 0;; *-T*) printf 'passwordauthentication no\\nkbdinteractiveauthentication no\\npubkeyauthentication yes\\nhostkey "+paths[bootstrapGuestIdentityHostKeyPath]+"\\n';; *) exit 74;; esac\n")
	writeGuestIdentityShim(t, shimDir, "systemctl", "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$GUEST_FIXTURE_SYSTEMCTL_LOG\"\n")
	log := filepath.Join(root, "systemctl.log")
	cmd := exec.Command("sh", "-c", script)
	cmd.Env = []string{"PATH=" + shimDir + ":/usr/bin:/bin", "LANG=C", "GUEST_FIXTURE_SYSTEMCTL_LOG=" + log}
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fixture installer failed: %v; output=%q", err, output)
	} else if !strings.Contains(string(output), markers[5]) {
		t.Fatalf("successful fixture install omitted completion marker %q", markers[5])
	}
	if got, err := os.ReadFile(paths[bootstrapGuestIdentityAuthorizedKeysPath]); err != nil || string(got) != "existing-key-line\n"+key+"\n" {
		t.Fatalf("authorized_keys = %q, err=%v", got, err)
	}
	if got, err := os.ReadFile(paths[bootstrapGuestIdentityDropinPath]); err != nil || !strings.Contains(string(got), "PasswordAuthentication no") || !strings.Contains(string(got), "KbdInteractiveAuthentication no") {
		t.Fatalf("sshd drop-in = %q, err=%v", got, err)
	}
	markerBytes, err := os.ReadFile(paths[bootstrapGuestIdentityMarkerPath])
	if err != nil {
		t.Fatalf("verified binding marker missing: %v", err)
	}
	if !bootstrapGuestIdentityMarkerMatches(markerBytes, binding, key) {
		t.Fatalf("persisted binding marker does not match canonical binding data: %s", markerBytes)
	}
	if info, err := os.Stat(paths[bootstrapGuestIdentityMarkerPath]); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("binding marker mode = %v, err=%v; want 0600", info, err)
	}
	if _, err := os.Stat(log); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("GuestIdentity called a service action; log stat err=%v", err)
	}
	for _, marker := range markers {
		if !validGuestIdentityMarker(marker) {
			t.Fatalf("invalid completion marker %q", marker)
		}
	}
}

func TestBootstrapGuestIdentityCommandRefusesExistingArtifactsAndDoesNotCompleteOnFailure(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("safe command execution fixture requires Linux shell semantics")
	}
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("safe command execution fixture requires sh")
	}
	for _, artifact := range []string{bootstrapGuestIdentityHostKeyPath, bootstrapGuestIdentityHostPublicKeyPath, bootstrapGuestIdentityDropinPath, bootstrapGuestIdentityMarkerPath} {
		t.Run(filepath.Base(artifact), func(t *testing.T) {
			binding := store.SessionRuntimeBinding{ID: "binding-fixture", SessionID: "session-a", EpochID: "epoch-a", Generation: "gen-a", VMID: 4002}
			markers, command, err := makeBootstrapGuestIdentityCommand(binding, publicMaterialFixture(t))
			if err != nil {
				t.Fatal(err)
			}
			script := decodeGuestIdentityScript(t, command)
			root := t.TempDir()
			paths := makeGuestIdentityFixturePaths(t, root)
			script = rewriteGuestIdentityFixturePaths(t, script, paths)
			shimDir := filepath.Join(root, "bin")
			if err := os.Mkdir(shimDir, 0700); err != nil {
				t.Fatal(err)
			}
			writeGuestIdentityShimsMinimal(t, shimDir, binding)
			artifactPath := paths[artifact]
			if err := os.MkdirAll(filepath.Dir(artifactPath), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(artifactPath, []byte("sentinel"), 0600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("sh", "-c", script)
			cmd.Env = []string{"PATH=" + shimDir + ":/usr/bin:/bin", "LANG=C", "GUEST_FIXTURE_SYSTEMCTL_LOG=" + filepath.Join(root, "service.log")}
			if output, runErr := cmd.CombinedOutput(); runErr == nil {
				t.Fatalf("existing artifact accepted; output=%q", output)
			} else if strings.Contains(string(output), markers[5]) {
				t.Fatalf("failed install emitted completion marker %q", markers[5])
			}
			if got, readErr := os.ReadFile(artifactPath); readErr != nil || string(got) != "sentinel" {
				t.Fatalf("existing artifact changed: %q, err=%v", got, readErr)
			}
			if _, statErr := os.Stat(filepath.Join(root, "service.log")); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("failed install reached service action; stat err=%v", statErr)
			}
		})
	}
}

func TestBootstrapGuestIdentityCommandRejectsHardLinkedAuthorizedKeysBeforeMutation(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("safe command execution fixture requires Linux shell semantics")
	}
	binding := store.SessionRuntimeBinding{ID: "binding-fixture", SessionID: "session-a", EpochID: "epoch-a", Generation: "gen-a", VMID: 4002}
	key := publicMaterialFixture(t)
	_, command, err := makeBootstrapGuestIdentityCommand(binding, key)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	paths := makeGuestIdentityFixturePaths(t, root)
	script := rewriteGuestIdentityFixturePaths(t, decodeGuestIdentityScript(t, command), paths)
	linked := filepath.Join(root, "outside-authorized-keys")
	if err := os.Link(paths[bootstrapGuestIdentityAuthorizedKeysPath], linked); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(paths[bootstrapGuestIdentityAuthorizedKeysPath])
	if err != nil {
		t.Fatal(err)
	}
	shimDir := filepath.Join(root, "bin")
	if err := os.Mkdir(shimDir, 0700); err != nil {
		t.Fatal(err)
	}
	writeGuestIdentityShimsMinimal(t, shimDir, binding)
	cmd := exec.Command("sh", "-c", script)
	cmd.Env = []string{"PATH=" + shimDir + ":/usr/bin:/bin", "LANG=C"}
	if out, err := cmd.CombinedOutput(); err == nil || strings.Contains(string(out), "_END__") {
		t.Fatalf("installer accepted linked authorized_keys or reported completion: %q, %v", out, err)
	}
	after, err := os.Stat(paths[bootstrapGuestIdentityAuthorizedKeysPath])
	if err != nil {
		t.Fatal(err)
	}
	if after.Mode().Perm() != before.Mode().Perm() || after.Size() != before.Size() {
		t.Fatalf("linked authorized_keys changed before refusal: before=%v/%d after=%v/%d", before.Mode().Perm(), before.Size(), after.Mode().Perm(), after.Size())
	}
	if got, err := os.ReadFile(linked); err != nil || string(got) != "existing-key-line\n" {
		t.Fatalf("outside hardlink changed: %q, %v", got, err)
	}
}

func TestBootstrapGuestIdentityCommandPreflightsArtifactsBeforeCreatingDirectories(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("safe command execution fixture requires Linux shell semantics")
	}
	binding := store.SessionRuntimeBinding{ID: "binding-fixture", SessionID: "session-a", EpochID: "epoch-a", Generation: "gen-a", VMID: 4002}
	_, command, err := makeBootstrapGuestIdentityCommand(binding, publicMaterialFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	paths := makeGuestIdentityFixturePaths(t, root)
	if err := os.RemoveAll(filepath.Join(root, "etc", "codex-session")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths[bootstrapGuestIdentityHostKeyPath], []byte("sentinel"), 0600); err != nil {
		t.Fatal(err)
	}
	script := rewriteGuestIdentityFixturePaths(t, decodeGuestIdentityScript(t, command), paths)
	shimDir := filepath.Join(root, "bin")
	if err := os.Mkdir(shimDir, 0700); err != nil {
		t.Fatal(err)
	}
	writeGuestIdentityShimsMinimal(t, shimDir, binding)
	cmd := exec.Command("sh", "-c", script)
	cmd.Env = []string{"PATH=" + shimDir + ":/usr/bin:/bin", "LANG=C"}
	if out, err := cmd.CombinedOutput(); err == nil || strings.Contains(string(out), "_END__") {
		t.Fatalf("installer accepted pre-existing identity artifact: %q, %v", out, err)
	}
	if _, err := os.Stat(filepath.Join(root, "etc", "codex-session")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("installer created session directory before rejecting artifact: %v", err)
	}
	if got, err := os.ReadFile(paths[bootstrapGuestIdentityHostKeyPath]); err != nil || string(got) != "sentinel" {
		t.Fatalf("existing identity artifact changed: %q, %v", got, err)
	}
}

func decodeGuestIdentityScript(t *testing.T, command string) string {
	t.Helper()
	if !strings.HasPrefix(command, "printf %s '") || !strings.HasSuffix(command, "' | base64 -d | sh\n") {
		t.Fatalf("unexpected fixed command envelope: %q", command)
	}
	encoded := strings.TrimSuffix(strings.TrimPrefix(command, "printf %s '"), "' | base64 -d | sh\n")
	decoded, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	return string(decoded)
}

func makeGuestIdentityFixturePaths(t *testing.T, root string) map[string]string {
	t.Helper()
	paths := map[string]string{
		"/etc":                                   filepath.Join(root, "etc"),
		bootstrapGuestIdentityHostKeyPath:        filepath.Join(root, "etc", "ssh", "ssh_host_p28_ed25519_key"),
		bootstrapGuestIdentityHostPublicKeyPath:  filepath.Join(root, "etc", "ssh", "ssh_host_p28_ed25519_key.pub"),
		bootstrapGuestIdentityDropinPath:         filepath.Join(root, "etc", "ssh", "sshd_config.d", "00-p28-session.conf"),
		bootstrapGuestIdentityMarkerPath:         filepath.Join(root, "etc", "codex-session", "bootstrap-identity.json"),
		"/root":                                  filepath.Join(root, "root"),
		bootstrapGuestIdentityAuthorizedKeysPath: filepath.Join(root, "root", ".ssh", "authorized_keys"),
	}
	if err := os.MkdirAll(filepath.Join(root, "etc", "ssh", "sshd_config.d"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "etc", "codex-session"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "root", ".ssh"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths[bootstrapGuestIdentityAuthorizedKeysPath], []byte("existing-key-line\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return paths
}

func rewriteGuestIdentityFixturePaths(t *testing.T, script string, paths map[string]string) string {
	t.Helper()
	keys := make([]string, 0, len(paths))
	for production := range paths {
		keys = append(keys, production)
	}
	sort.Slice(keys, func(i, j int) bool { return len(keys[i]) > len(keys[j]) })
	pairs := make([]string, 0, len(keys)*2)
	for _, production := range keys {
		pairs = append(pairs, production, paths[production])
	}
	rewritten := strings.NewReplacer(pairs...).Replace(script)
	residual := regexp.MustCompile(`(^|[\s"'=])/(etc|root)(/|[\s"']|$)`)
	if match := residual.FindString(rewritten); match != "" {
		t.Fatalf("fixture script still contains a production root token %q", match)
	}
	return rewritten
}

func writeGuestIdentityShim(t *testing.T, dir, name, body string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0700); err != nil {
		t.Fatal(err)
	}
}

func writeGuestIdentityShimsMinimal(t *testing.T, dir string, binding store.SessionRuntimeBinding) {
	t.Helper()
	writeGuestIdentityShim(t, dir, "id", "printf '0\\n'\n")
	writeGuestIdentityShim(t, dir, "uname", "printf 'Linux\\n'\n")
	writeGuestIdentityShim(t, dir, "hostname", "printf '%s\\n' '"+sessionHostname(binding.SessionID, binding.EpochID, binding.Generation)+"'\n")
	writeGuestIdentityShim(t, dir, "findmnt", "printf '/\\n'\n")
	writeGuestIdentityShim(t, dir, "stat", "if [ \"$1\" = -c ] && [ \"$2\" = %u ]; then printf '0\\n'; elif [ \"$1\" = -c ] && [ \"$2\" = '%u %h' ]; then links=$(/usr/bin/stat -c %h -- \"$4\") || exit 1; printf '0 %s\\n' \"$links\"; else exec /usr/bin/stat \"$@\"; fi\n")
	writeGuestIdentityShim(t, dir, "ssh-keygen", "exit 73\n")
	writeGuestIdentityShim(t, dir, "sshd", "exit 0\n")
	writeGuestIdentityShim(t, dir, "systemctl", "exit 74\n")
}
