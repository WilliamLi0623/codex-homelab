package sessionruntime

import (
	"context"
	"encoding/base64"
	"regexp"
	"strings"
	"testing"

	"github.com/WilliamLi0623/codex-homelab/internal/store"
	"golang.org/x/net/websocket"
)

func TestSanitizeBootstrapImageUsesFencedConsoleAndReturnsBoundEvidence(t *testing.T) {
	options := &bootstrapHostKeyConsoleOptions{binding: store.SessionRuntimeBinding{ID: "binding", SessionID: "session", EpochID: "epoch", Generation: "generation", VMID: 4002}}
	options.terminal = func(conn *websocket.Conn, command string) {
		markers := imageSanitationMarkersFromCommand(t, command)
		script := bootstrapTLSArchiveScriptFromCommand(t, command)
		roots, _ := bootstrapArchivePolicyRoots(bootstrapArchivePolicySessionImageCredentialsV2)
		if !strings.Contains(script, "rm -- ") {
			t.Error("sanitation console command did not include exact-root deletion")
		}
		for _, root := range roots {
			if !strings.Contains(script, " /"+root) {
				t.Errorf("sanitation console command omitted /%s", root)
			}
		}
		output := []string{markers[0], "0", markers[1], "Linux", markers[2], options.identity[2], markers[3], markers[4], "absent=23", markers[5]}
		if err := websocket.Message.Send(conn, []byte(strings.Join(output, "\n")+"\n")); err != nil {
			t.Errorf("send sanitation transcript: %v", err)
		}
	}
	runtime, server := newBootstrapHostKeyRuntime(t, options)
	defer server.Close()
	result, err := runtime.sanitizeBootstrapImage(context.Background(), options.binding)
	if err != nil {
		t.Fatalf("sanitizeBootstrapImage(): %v", err)
	}
	if result.UID != 0 || result.OS != "Linux" || result.Hostname != sessionHostname(options.binding.SessionID, options.binding.EpochID, options.binding.Generation) || result.Policy != "session-image-credentials-v2" || !bootstrapDigestPattern.MatchString(result.IsolationSHA256) {
		t.Fatalf("sanitation result = %+v", result)
	}
	if options.configCalls != 2 || options.consolePosts != 1 || options.websocketHits != 1 {
		t.Fatalf("fence reads=%d termproxy=%d websocket=%d; want 2,1,1", options.configCalls, options.consolePosts, options.websocketHits)
	}
}

func TestObserveBootstrapImageSanitationUsesOnlyReadOnlyConsoleCommand(t *testing.T) {
	options := &bootstrapHostKeyConsoleOptions{binding: store.SessionRuntimeBinding{ID: "binding", SessionID: "session", EpochID: "epoch", Generation: "generation", VMID: 4002}}
	options.terminal = func(conn *websocket.Conn, command string) {
		markers := imageSanitationMarkersFromCommand(t, command)
		script := bootstrapTLSArchiveScriptFromCommand(t, command)
		for _, forbidden := range []string{"rm --", "unlink", "mv ", "touch ", ">>"} {
			if strings.Contains(script, forbidden) {
				t.Errorf("read-only console command contains %q", forbidden)
			}
		}
		output := []string{markers[0], "0", markers[1], "Linux", markers[2], options.identity[2], markers[3], markers[4], "absent=23", markers[5]}
		_ = websocket.Message.Send(conn, []byte(strings.Join(output, "\n")+"\n"))
	}
	runtime, server := newBootstrapHostKeyRuntime(t, options)
	defer server.Close()
	result, err := runtime.observeBootstrapImageSanitation(context.Background(), options.binding)
	if err != nil || result.Policy != "session-image-credentials-v2" {
		t.Fatalf("observe sanitation = %+v, err=%v", result, err)
	}
}

func TestImageSanitationConsoleRejectsWrongGuestIdentity(t *testing.T) {
	options := &bootstrapHostKeyConsoleOptions{binding: store.SessionRuntimeBinding{ID: "binding", SessionID: "session", EpochID: "epoch", Generation: "generation", VMID: 4002}, identity: [3]string{"0", "Linux", "wrong-host"}}
	options.terminal = func(conn *websocket.Conn, command string) {
		markers := imageSanitationMarkersFromCommand(t, command)
		output := []string{markers[0], "0", markers[1], "Linux", markers[2], "wrong-host", markers[3], markers[4], "absent=23", markers[5]}
		_ = websocket.Message.Send(conn, []byte(strings.Join(output, "\n")+"\n"))
	}
	runtime, server := newBootstrapHostKeyRuntime(t, options)
	defer server.Close()
	if _, err := runtime.observeBootstrapImageSanitation(context.Background(), options.binding); err == nil {
		t.Fatal("wrong-generation hostname accepted by sanitation observer")
	}
}

func imageSanitationMarkersFromCommand(t *testing.T, command string) [6]string {
	t.Helper()
	script := bootstrapTLSArchiveScriptFromCommand(t, command)
	matches := regexp.MustCompile(`__CODEX_IMAGESAN_[0-9a-f]+_[A-Z]+__`).FindAllString(script, -1)
	var markers [6]string
	if len(matches) != len(markers) {
		t.Fatalf("sanitation command has %d markers, want %d", len(matches), len(markers))
	}
	copy(markers[:], matches)
	return markers
}

func TestBootstrapImageSanitizeCommandDeletesOnlyV2RootsAfterPreflight(t *testing.T) {
	binding := store.SessionRuntimeBinding{ID: "binding", SessionID: "session", EpochID: "epoch", Generation: "generation", VMID: 4002}
	markers, command, err := makeBootstrapImageSanitizeCommand(binding, bootstrapArchivePolicySessionImageCredentialsV2)
	if err != nil {
		t.Fatalf("makeBootstrapImageSanitizeCommand(): %v", err)
	}
	script := decodeFixedBootstrapCommand(t, command)
	roots, _ := bootstrapArchivePolicyRoots(bootstrapArchivePolicySessionImageCredentialsV2)
	deleteAt := strings.Index(script, "rm -- ")
	if deleteAt < 0 {
		t.Fatalf("fixed sanitation script lacks literal rm argument list: %q", script)
	}
	preflightAt := strings.Index(script, markers[3])
	if preflightAt < 0 || preflightAt > deleteAt {
		t.Fatal("sanitation command does not emit preflight-complete marker before removal")
	}
	deleteEnd := strings.Index(script[deleteAt:], "\n")
	if deleteEnd < 0 {
		t.Fatal("literal rm argument list is not line-bounded")
	}
	gotPaths := strings.Fields(strings.TrimPrefix(script[deleteAt:deleteAt+deleteEnd], "rm -- "))
	if len(gotPaths) != len(roots) {
		t.Fatalf("rm path count = %d, want %d: %q", len(gotPaths), len(roots), gotPaths)
	}
	for i, root := range roots {
		path := "/" + root
		if gotPaths[i] != path {
			t.Fatalf("rm path[%d] = %q, want %q", i, gotPaths[i], path)
		}
		if !strings.Contains(script[:deleteAt], "target=\""+path+"\"") || !strings.Contains(script[:deleteAt], "[ -f \"$target\" ]") || !strings.Contains(script[:deleteAt], "[ ! -L \"$target\" ]") {
			t.Fatalf("preflight does not require regular non-symlink %s", path)
		}
		if !strings.Contains(script[:deleteAt], "target=\""+path+"\"") || !strings.Contains(script[deleteAt+deleteEnd:], "[ ! -e \""+path+"\" ]") {
			t.Fatalf("mountpoint preflight or absent readback missing for %s", path)
		}
	}
	if !strings.Contains(command, "bash -o pipefail") || !strings.Contains(script, "sync -f /") || strings.Contains(script, "* ") || strings.Contains(script, "find / ") {
		t.Fatal("sanitation script lacks failure fencing/durability or uses a broad path operation")
	}
	for _, marker := range markers {
		if !validArchiveMarker(marker) {
			t.Fatalf("invalid console marker %q", marker)
		}
	}
	if _, _, err := makeBootstrapImageSanitizeCommand(binding, bootstrapArchivePolicyTLS); err == nil {
		t.Fatal("TLS-only policy authorized Session-image sanitation")
	}
}

func TestBootstrapImageSanitationObserverIsReadOnlyAndRequiresAllV2RootsAbsent(t *testing.T) {
	binding := store.SessionRuntimeBinding{ID: "binding", SessionID: "session", EpochID: "epoch", Generation: "generation", VMID: 4002}
	markers, command, err := makeBootstrapImageSanitationObserverCommand(binding, bootstrapArchivePolicySessionImageCredentialsV2)
	if err != nil {
		t.Fatalf("makeBootstrapImageSanitationObserverCommand(): %v", err)
	}
	script := decodeFixedBootstrapCommand(t, command)
	roots, _ := bootstrapArchivePolicyRoots(bootstrapArchivePolicySessionImageCredentialsV2)
	for _, root := range roots {
		path := "/" + root
		if !strings.Contains(script, "[ ! -e \""+path+"\" ]") || !strings.Contains(script, "[ ! -L \""+path+"\" ]") {
			t.Fatalf("read-only observer does not require %s absent and non-symlink", path)
		}
	}
	for _, forbidden := range []string{"rm ", "unlink", "mv ", "install ", "ssh-keygen", ">>", "touch "} {
		if strings.Contains(script, forbidden) {
			t.Fatalf("observer contains mutation %q", forbidden)
		}
	}
	if !strings.Contains(script, markers[5]) || !strings.Contains(script, "hostname") {
		t.Fatal("observer lacks terminal marker or generation-bound identity check")
	}
	if _, _, err := makeBootstrapImageSanitationObserverCommand(binding, bootstrapArchivePolicySessionImageCredentials); err == nil {
		t.Fatal("observer accepted obsolete v1 policy")
	}
}

func decodeFixedBootstrapCommand(t *testing.T, command string) string {
	t.Helper()
	start, end := strings.IndexByte(command, '\''), strings.LastIndexByte(command, '\'')
	if start < 0 || end <= start {
		t.Fatalf("fixed command has no single-quoted encoded script: %q", command)
	}
	decoded, err := base64.StdEncoding.DecodeString(command[start+1 : end])
	if err != nil {
		t.Fatalf("decode fixed command: %v", err)
	}
	return string(decoded)
}
