package sessionruntime

import (
	"context"
	"encoding/base64"
	"errors"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/WilliamLi0623/codex-homelab/internal/store"
	"golang.org/x/net/websocket"
)

func TestObserveBootstrapGuestIdentityVerifiesExactInstalledState(t *testing.T) {
	options := &bootstrapHostKeyConsoleOptions{}
	runtime, server := newGuestIdentityConsoleRuntime(t, options)
	defer server.Close()
	clientKey := publicMaterialFixture(t)
	marker, err := bootstrapGuestIdentityMarkerData(options.binding, clientKey)
	if err != nil {
		t.Fatal(err)
	}
	options.terminal = func(conn *websocket.Conn, frame string) {
		markers, ok := guestIdentityObserveMarkersFromFrame(frame)
		if !ok {
			t.Errorf("console input was not the fixed identity observer command")
			return
		}
		if strings.Contains(frame, clientKey) {
			t.Errorf("client key appeared outside encoded fixed-command payload")
		}
		output := guestIdentityObserveTranscript(markers,
			[3]string{"0", "Linux", sessionHostname(options.binding.SessionID, options.binding.EpochID, options.binding.Generation)},
			marker, options.publicKey, options.publicKey, "1", validGuestIdentitySSHDOutput(),
		)
		_ = websocket.Message.Send(conn, []byte(output))
	}

	result, err := runtime.ObserveBootstrapGuestIdentity(context.Background(), options.binding, clientKey)
	if err != nil {
		t.Fatalf("ObserveBootstrapGuestIdentity() error = %v", err)
	}
	if result.UID != 0 || result.OS != "Linux" || result.Hostname != sessionHostname(options.binding.SessionID, options.binding.EpochID, options.binding.Generation) {
		t.Fatalf("ObserveBootstrapGuestIdentity() = %+v; identity mismatch", result)
	}
	if !validBootstrapEvidence(store.SessionBootstrapEvidence{SHA256: result.IsolationSHA256}) {
		t.Fatal("observation returned invalid isolation evidence")
	}
	if options.configCalls != 2 || options.consolePosts != 1 || options.websocketHits != 1 {
		t.Fatalf("config reads=%d termproxy posts=%d sockets=%d; want before/after isolation around one observation", options.configCalls, options.consolePosts, options.websocketHits)
	}
	script, ok := guestIdentityObserveScriptFromFrame(options.output)
	if !ok || !strings.Contains(script, "set -o pipefail") || !strings.Contains(script, "sshd -t") || !strings.Contains(script, "sshd -T") || !strings.Contains(script, "ssh-keygen -y -f /etc/ssh/ssh_host_p28_ed25519_key") {
		t.Fatal("observer did not send its fixed read-only identity checks")
	}
	for _, mutation := range []string{"systemctl", "service ", "mkdir ", "chmod ", " >>", "rm -", "touch "} {
		if strings.Contains(script, mutation) {
			t.Fatalf("read-only observer command contains mutation %q", mutation)
		}
	}
}

func TestObserveBootstrapGuestIdentityRejectsStaleBindingOrClientKey(t *testing.T) {
	t.Run("stale generation before console", func(t *testing.T) {
		options := &bootstrapHostKeyConsoleOptions{}
		runtime, server := newGuestIdentityConsoleRuntime(t, options)
		defer server.Close()
		stale := options.binding
		stale.Generation = "old-generation"
		_, err := runtime.ObserveBootstrapGuestIdentity(context.Background(), stale, publicMaterialFixture(t))
		if !errors.Is(err, ErrBootstrapHostKeyIsolation) || options.consolePosts != 0 || options.websocketHits != 0 {
			t.Fatalf("stale generation error=%v posts=%d sockets=%d", err, options.consolePosts, options.websocketHits)
		}
	})
	t.Run("marker for another client key", func(t *testing.T) {
		options := &bootstrapHostKeyConsoleOptions{}
		runtime, server := newGuestIdentityConsoleRuntime(t, options)
		defer server.Close()
		clientKey := publicMaterialFixture(t)
		staleKey := publicMaterialFixture(t)
		staleMarker, markerErr := bootstrapGuestIdentityMarkerData(options.binding, staleKey)
		if markerErr != nil {
			t.Fatal(markerErr)
		}
		options.terminal = func(conn *websocket.Conn, frame string) {
			markers, ok := guestIdentityObserveMarkersFromFrame(frame)
			if ok {
				_ = websocket.Message.Send(conn, []byte(guestIdentityObserveTranscript(markers,
					[3]string{"0", "Linux", sessionHostname(options.binding.SessionID, options.binding.EpochID, options.binding.Generation)},
					staleMarker, options.publicKey, options.publicKey, "1", validGuestIdentitySSHDOutput())))
			}
		}
		_, err := runtime.ObserveBootstrapGuestIdentity(context.Background(), options.binding, clientKey)
		if !errors.Is(err, ErrConsoleIdentityMismatch) {
			t.Fatalf("stale client key marker error=%v; want identity mismatch", err)
		}
	})
}

func TestObserveBootstrapGuestIdentityBoundsGuestSideCommandOutput(t *testing.T) {
	binding := store.SessionRuntimeBinding{ID: "binding", SessionID: "session", EpochID: "epoch", Generation: "generation", VMID: 4002}
	markers, command, _, err := makeBootstrapGuestIdentityObserveCommand(binding, publicMaterialFixture(t))
	if err != nil || !validBootstrapGuestIdentityObserveMarkers(markers) {
		t.Fatalf("command generation markers=%q err=%v", markers, err)
	}
	script := decodeGuestIdentityScript(t, command)
	for _, required := range []string{"capture_bounded", "head -c", "marker_size", "pub_size", "authorized_size", "ulimit -v 131072", "capture_bounded \"$CAPTURE_TOKEN\" 8192 base64 -w 0", "capture_bounded \"$CAPTURE_TOKEN\" 4096 ssh-keygen -y", "capture_bounded \"$CAPTURE_TOKEN\" 8192 sshd -T"} {
		if !strings.Contains(script, required) {
			t.Errorf("observer script lacks guest-side bound %q", required)
		}
	}
	if strings.Index(script, "capture_bounded() {") > strings.Index(script, "capture_bounded \"$CAPTURE_TOKEN\" 8192 base64 -w 0") || strings.Index(script, "capture_bounded() {") > strings.Index(script, "capture_bounded \"$CAPTURE_TOKEN\" 8192 sshd -T") {
		t.Fatal("observer bounded capture helper is declared after the commands using it")
	}
}

func TestBootstrapGuestIdentityCaptureBoundedCapsOutputAndPreservesFailure(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("capture helper execution requires Linux shell semantics")
	}
	binding := store.SessionRuntimeBinding{ID: "binding", SessionID: "session", EpochID: "epoch", Generation: "generation", VMID: 4002}
	_, command, _, err := makeBootstrapGuestIdentityObserveCommand(binding, publicMaterialFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	script := decodeGuestIdentityScript(t, command)
	var helper string
	var pipefail string
	for _, line := range strings.Split(script, "\n") {
		if strings.HasPrefix(line, "capture_bounded() {") {
			helper = line
		}
		if line == "set -o pipefail" {
			pipefail = line
		}
	}
	if helper == "" || pipefail == "" {
		t.Fatal("capture helper or required pipefail mode missing")
	}
	probe := "set -eu\n" + pipefail + "\n" + helper + "\n" +
		"capture_bounded __CAPTURE_TEST__ 5 printf hello\n[ \"$CAPTURED\" = hello ]\n" +
		"if capture_bounded __CAPTURE_TEST__ 3 printf hello; then exit 11; fi\n" +
		"if capture_bounded __CAPTURE_TEST__ 5 sh -c 'printf nope; exit 3'; then exit 12; fi\n" +
		"if capture_bounded __CAPTURE_TEST__ 64 sh -c 'printf __CAPTURE_TEST__; exit 3'; then exit 13; fi\n"
	cmd := exec.Command("bash", "-c", probe)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("capture helper did not enforce its limit/status: %v; output=%q", err, output)
	}
}

func TestObserveBootstrapGuestIdentityRejectsIncorrectGuestFacts(t *testing.T) {
	for _, tc := range []struct {
		name     string
		identity [3]string
		derived  string
		stored   string
		keyCount string
		sshd     string
		want     error
	}{
		{name: "non-root", identity: [3]string{"1000", "Linux", sessionHostname("session-a", "epoch-a", "gen-1")}, want: ErrConsoleIdentityMismatch},
		{name: "wrong OS", identity: [3]string{"0", "FreeBSD", sessionHostname("session-a", "epoch-a", "gen-1")}, want: ErrConsoleIdentityMismatch},
		{name: "wrong hostname", identity: [3]string{"0", "Linux", "unbound-host"}, want: ErrConsoleIdentityMismatch},
		{name: "derived host key differs", derived: "ssh-ed25519 DIFFERENT_PUBLIC_KEY", want: ErrConsoleMalformedOutput},
		{name: "authorized key absent", keyCount: "0", want: ErrConsoleMalformedOutput},
		{name: "authorized key duplicated", keyCount: "2", want: ErrConsoleMalformedOutput},
		{name: "password enabled", sshd: "hostkey /etc/ssh/ssh_host_p28_ed25519_key\npasswordauthentication yes\nkbdinteractiveauthentication no\npubkeyauthentication yes", want: ErrConsoleMalformedOutput},
		{name: "extra effective host key", sshd: validGuestIdentitySSHDOutput() + "\nhostkey /etc/ssh/ssh_host_ed25519_key", want: ErrConsoleMalformedOutput},
	} {
		t.Run(tc.name, func(t *testing.T) {
			options := &bootstrapHostKeyConsoleOptions{}
			runtime, server := newGuestIdentityConsoleRuntime(t, options)
			defer server.Close()
			clientKey := publicMaterialFixture(t)
			marker, err := bootstrapGuestIdentityMarkerData(options.binding, clientKey)
			if err != nil {
				t.Fatal(err)
			}
			if tc.identity == [3]string{} {
				tc.identity = [3]string{"0", "Linux", sessionHostname(options.binding.SessionID, options.binding.EpochID, options.binding.Generation)}
			}
			if tc.derived == "" {
				tc.derived = options.publicKey
			}
			if tc.stored == "" {
				tc.stored = options.publicKey
			}
			if tc.keyCount == "" {
				tc.keyCount = "1"
			}
			if tc.sshd == "" {
				tc.sshd = validGuestIdentitySSHDOutput()
			}
			options.terminal = func(conn *websocket.Conn, frame string) {
				markers, ok := guestIdentityObserveMarkersFromFrame(frame)
				if ok {
					output := guestIdentityObserveTranscript(markers, tc.identity, marker, tc.derived, tc.stored, tc.keyCount, tc.sshd)
					_ = websocket.Message.Send(conn, []byte(output))
				}
			}
			_, err = runtime.ObserveBootstrapGuestIdentity(context.Background(), options.binding, clientKey)
			if !errors.Is(err, tc.want) {
				t.Fatalf("ObserveBootstrapGuestIdentity() error=%v; want %v", err, tc.want)
			}
			for _, privateValue := range []string{bootstrapHostKeyTestToken, "ticket-secret", "DIFFERENT_PUBLIC_KEY", "ssh_host_ed25519_key"} {
				if strings.Contains(err.Error(), privateValue) {
					t.Fatal("observer error leaked terminal, key, or authentication detail")
				}
			}
		})
	}
}

func TestObserveBootstrapGuestIdentityRequiresCompletionAndUnchangedFence(t *testing.T) {
	t.Run("missing completion marker", func(t *testing.T) {
		options := &bootstrapHostKeyConsoleOptions{}
		runtime, server := newGuestIdentityConsoleRuntime(t, options)
		defer server.Close()
		clientKey := publicMaterialFixture(t)
		marker, err := bootstrapGuestIdentityMarkerData(options.binding, clientKey)
		if err != nil {
			t.Fatal(err)
		}
		options.terminal = func(conn *websocket.Conn, frame string) {
			markers, ok := guestIdentityObserveMarkersFromFrame(frame)
			if ok {
				output := guestIdentityObserveTranscript(markers, [3]string{"0", "Linux", sessionHostname(options.binding.SessionID, options.binding.EpochID, options.binding.Generation)}, marker, options.publicKey, options.publicKey, "1", validGuestIdentitySSHDOutput())
				_ = websocket.Message.Send(conn, []byte(strings.TrimSuffix(output, markers[len(markers)-1]+"\n")))
			}
		}
		_, err = runtime.ObserveBootstrapGuestIdentity(context.Background(), options.binding, clientKey)
		if !errors.Is(err, ErrConsoleMalformedOutput) {
			t.Fatalf("missing completion marker error=%v; want malformed output", err)
		}
	})
	t.Run("changed isolation", func(t *testing.T) {
		options := &bootstrapHostKeyConsoleOptions{loseFenceAfterConsole: true}
		runtime, server := newGuestIdentityConsoleRuntime(t, options)
		defer server.Close()
		clientKey := publicMaterialFixture(t)
		marker, err := bootstrapGuestIdentityMarkerData(options.binding, clientKey)
		if err != nil {
			t.Fatal(err)
		}
		options.terminal = func(conn *websocket.Conn, frame string) {
			markers, ok := guestIdentityObserveMarkersFromFrame(frame)
			if ok {
				_ = websocket.Message.Send(conn, []byte(guestIdentityObserveTranscript(markers, [3]string{"0", "Linux", sessionHostname(options.binding.SessionID, options.binding.EpochID, options.binding.Generation)}, marker, options.publicKey, options.publicKey, "1", validGuestIdentitySSHDOutput())))
			}
		}
		_, err = runtime.ObserveBootstrapGuestIdentity(context.Background(), options.binding, clientKey)
		if !errors.Is(err, ErrBootstrapHostKeyIsolation) || options.configCalls != 2 {
			t.Fatalf("changed isolation error=%v config reads=%d", err, options.configCalls)
		}
	})
	for _, tc := range []struct {
		name     string
		terminal func(*websocket.Conn, string)
		want     error
	}{
		{name: "outage", terminal: func(conn *websocket.Conn, _ string) { _ = conn.Close() }, want: ErrConsoleDisconnected},
		{name: "output limit", terminal: func(conn *websocket.Conn, _ string) {
			_ = websocket.Message.Send(conn, []byte("RAW_TERMINAL_SECRET"+strings.Repeat("x", consoleFrameLimit)))
		}, want: ErrConsoleOutputLimit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			options := &bootstrapHostKeyConsoleOptions{}
			runtime, server := newGuestIdentityConsoleRuntime(t, options)
			defer server.Close()
			options.terminal = tc.terminal
			_, err := runtime.ObserveBootstrapGuestIdentity(context.Background(), options.binding, publicMaterialFixture(t))
			if !errors.Is(err, tc.want) {
				t.Fatalf("observer error=%v; want %v", err, tc.want)
			}
			if strings.Contains(err.Error(), "RAW_TERMINAL_SECRET") {
				t.Fatal("observer error leaked raw terminal output")
			}
		})
	}
}

func guestIdentityObserveTranscript(markers [10]string, identity [3]string, marker []byte, derived, stored, keyCount, sshd string) string {
	return strings.Join([]string{
		markers[0], markers[1], identity[0], markers[2], identity[1], markers[3], identity[2],
		markers[4], base64.StdEncoding.EncodeToString(marker), markers[5], derived, markers[6], stored,
		markers[7], keyCount, markers[8], sshd, markers[9],
	}, "\n") + "\n"
}

func guestIdentityObserveMarkersFromFrame(frame string) ([10]string, bool) {
	var markers [10]string
	script, ok := guestIdentityObserveScriptFromFrame(frame)
	if !ok {
		return markers, false
	}
	re := regexp.MustCompile(`(__CODEX_GUESTOBS_[a-f0-9]{32}_(?:BEGIN|UID|OS|HOST|MARKER|DERIVED|STORED|AUTHORIZED|SSHD|END)__)`)
	matches := re.FindAllStringSubmatch(script, -1)
	if len(matches) != len(markers) {
		return [10]string{}, false
	}
	for i, match := range matches {
		markers[i] = match[1]
		for j := 0; j < i; j++ {
			if markers[j] == markers[i] {
				return [10]string{}, false
			}
		}
	}
	return markers, true
}

func guestIdentityObserveScriptFromFrame(frame string) (string, bool) {
	if !strings.HasPrefix(frame, "0:") {
		return "", false
	}
	lengthText, command, ok := strings.Cut(strings.TrimPrefix(frame, "0:"), ":")
	length, err := strconv.Atoi(lengthText)
	if !ok || err != nil || length != len([]byte(command)) || !strings.HasPrefix(command, "printf %s '") || !strings.HasSuffix(command, "' | base64 -d | bash\n") {
		return "", false
	}
	encoded := strings.TrimSuffix(strings.TrimPrefix(command, "printf %s '"), "' | base64 -d | bash\n")
	script, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil {
		return "", false
	}
	return string(script), true
}

func validGuestIdentitySSHDOutput() string {
	return "hostkey /etc/ssh/ssh_host_p28_ed25519_key\npasswordauthentication no\nkbdinteractiveauthentication no\npubkeyauthentication yes"
}
