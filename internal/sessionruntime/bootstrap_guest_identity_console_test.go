package sessionruntime

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/WilliamLi0623/codex-homelab/internal/store"
	"golang.org/x/net/websocket"
)

func TestInstallBootstrapGuestIdentityUsesFixedCommandAndVerifiesTranscript(t *testing.T) {
	options := &bootstrapHostKeyConsoleOptions{}
	runtime, server := newGuestIdentityConsoleRuntime(t, options)
	defer server.Close()
	clientKey := publicMaterialFixture(t)
	options.terminal = func(conn *websocket.Conn, frame string) {
		markers, ok := guestIdentityMarkersFromTermproxyCommand(frame)
		if !ok {
			t.Errorf("console input was not the fixed GuestIdentity command")
			return
		}
		if strings.Contains(frame, clientKey) {
			t.Errorf("client public key appeared outside the base64 command payload")
		}
		_ = websocket.Message.Send(conn, []byte(guestIdentityTranscript(markers, [3]string{"0", "Linux", sessionHostname(options.binding.SessionID, options.binding.EpochID, options.binding.Generation)}, options.publicKey)))
	}

	result, err := runtime.InstallBootstrapGuestIdentity(context.Background(), options.binding, clientKey)
	if err != nil {
		t.Fatalf("InstallBootstrapGuestIdentity() error = %v", err)
	}
	if result.UID != 0 || result.OS != "Linux" || result.Hostname != sessionHostname(options.binding.SessionID, options.binding.EpochID, options.binding.Generation) {
		t.Fatalf("InstallBootstrapGuestIdentity() result = %+v; identity mismatch", result)
	}
	if !validBootstrapEvidence(store.SessionBootstrapEvidence{SHA256: result.IsolationSHA256}) {
		t.Fatalf("InstallBootstrapGuestIdentity() returned invalid isolation evidence")
	}
	if options.configCalls != 2 || options.consolePosts != 1 || options.websocketHits != 1 {
		t.Fatalf("config reads=%d termproxy posts=%d WebSocket dials=%d; want before/after isolation around one console", options.configCalls, options.consolePosts, options.websocketHits)
	}
}

func TestInstallBootstrapGuestIdentityRejectsUnverifiedTranscript(t *testing.T) {
	for _, tc := range []struct {
		name     string
		identity [3]string
		key      string
		mutate   func([6]string) string
		want     error
	}{
		{name: "non-root", identity: [3]string{"1000", "Linux", sessionHostname("session-a", "epoch-a", "gen-1")}, want: ErrConsoleIdentityMismatch},
		{name: "non-Linux", identity: [3]string{"0", "FreeBSD", sessionHostname("session-a", "epoch-a", "gen-1")}, want: ErrConsoleIdentityMismatch},
		{name: "wrong hostname", identity: [3]string{"0", "Linux", "unbound-host"}, want: ErrConsoleIdentityMismatch},
		{name: "invalid host public key", identity: [3]string{"0", "Linux", sessionHostname("session-a", "epoch-a", "gen-1")}, key: "ssh-ed25519 INVALID_PUBLIC_KEY", want: ErrConsoleMalformedOutput},
		{name: "wrong marker order", identity: [3]string{"0", "Linux", sessionHostname("session-a", "epoch-a", "gen-1")}, mutate: func(markers [6]string) string {
			return strings.Join([]string{markers[0], "0", markers[2]}, "\n") + "\n"
		}, want: ErrConsoleMalformedOutput},
		{name: "missing end marker", identity: [3]string{"0", "Linux", sessionHostname("session-a", "epoch-a", "gen-1")}, mutate: func(markers [6]string) string {
			return strings.Join([]string{markers[0], "0", markers[1], "Linux", markers[2], sessionHostname("session-a", "epoch-a", "gen-1"), markers[3]}, "\n") + "\n"
		}, want: ErrConsoleMalformedOutput},
	} {
		t.Run(tc.name, func(t *testing.T) {
			options := &bootstrapHostKeyConsoleOptions{}
			runtime, server := newGuestIdentityConsoleRuntime(t, options)
			defer server.Close()
			clientKey := publicMaterialFixture(t)
			if tc.identity == [3]string{} {
				tc.identity = [3]string{"0", "Linux", sessionHostname(options.binding.SessionID, options.binding.EpochID, options.binding.Generation)}
			}
			if tc.key == "" {
				tc.key = options.publicKey
			}
			options.terminal = func(conn *websocket.Conn, frame string) {
				markers, ok := guestIdentityMarkersFromTermproxyCommand(frame)
				if !ok {
					return
				}
				transcript := guestIdentityTranscript(markers, tc.identity, tc.key)
				if tc.mutate != nil {
					transcript = tc.mutate(markers)
					if tc.name == "missing end marker" {
						transcript = guestIdentityTranscript(markers, tc.identity, tc.key)
						transcript = strings.TrimSuffix(transcript, markers[5]+"\n")
					}
				}
				_ = websocket.Message.Send(conn, []byte(transcript))
			}
			_, err := runtime.InstallBootstrapGuestIdentity(context.Background(), options.binding, clientKey)
			if !errors.Is(err, tc.want) {
				t.Fatalf("InstallBootstrapGuestIdentity() error = %v; want %v", err, tc.want)
			}
			for _, secret := range []string{bootstrapHostKeyTestToken, "ticket-secret", "INVALID_PUBLIC_KEY", clientKey} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("error leaked terminal or key material")
				}
			}
		})
	}
}

func TestInstallBootstrapGuestIdentityRequiresOwnedRunningGuestAndUnchangedFence(t *testing.T) {
	for _, tc := range []struct {
		name    string
		options bootstrapHostKeyConsoleOptions
		want    error
	}{
		{name: "unfenced", options: bootstrapHostKeyConsoleOptions{unfenced: true}, want: ErrBootstrapHostKeyIsolation},
		{name: "stopped", options: bootstrapHostKeyConsoleOptions{state: "stopped"}, want: ErrConsoleNotRunning},
		{name: "fence changed after console", options: bootstrapHostKeyConsoleOptions{loseFenceAfterConsole: true}, want: ErrBootstrapHostKeyIsolation},
	} {
		t.Run(tc.name, func(t *testing.T) {
			options := tc.options
			runtime, server := newGuestIdentityConsoleRuntime(t, &options)
			defer server.Close()
			if tc.name == "fence changed after console" {
				options.terminal = func(conn *websocket.Conn, frame string) {
					markers, ok := guestIdentityMarkersFromTermproxyCommand(frame)
					if ok {
						_ = websocket.Message.Send(conn, []byte(guestIdentityTranscript(markers, [3]string{"0", "Linux", sessionHostname(options.binding.SessionID, options.binding.EpochID, options.binding.Generation)}, options.publicKey)))
					}
				}
			}
			_, err := runtime.InstallBootstrapGuestIdentity(context.Background(), options.binding, publicMaterialFixture(t))
			if !errors.Is(err, tc.want) {
				t.Fatalf("InstallBootstrapGuestIdentity() error = %v; want %v", err, tc.want)
			}
			if tc.name != "fence changed after console" && (options.consolePosts != 0 || options.websocketHits != 0) {
				t.Fatalf("rejected guest reached termproxy: posts=%d sockets=%d", options.consolePosts, options.websocketHits)
			}
		})
	}
}

func TestInstallBootstrapGuestIdentityRequiresShellConsoleMode(t *testing.T) {
	binding := store.SessionRuntimeBinding{ID: "binding-a", SessionID: "session-a", EpochID: "epoch-a", Generation: "gen-1", VMID: 4002}
	owner, err := encodeOwnership(sessionOwnership{Version: 1, SessionID: binding.SessionID, EpochID: binding.EpochID, Generation: binding.Generation})
	if err != nil {
		t.Fatal(err)
	}
	termproxyPosts := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/termproxy") {
			termproxyPosts++
		}
		if strings.HasSuffix(r.URL.Path, "/config") {
			data := map[string]any{"vmid": binding.VMID, "hostname": sessionHostname(binding.SessionID, binding.EpochID, binding.Generation), "description": owner, "unprivileged": 0, "onboot": 0, "cmode": "console", "rootfs": "local:4005/vm-4005-disk-0.raw,size=8G", "mp0": "pool:subvol-4005-disk-1,mp=/workspace", "net0": "name=eth0,bridge=vmbr0,ip=dhcp,link_down=1"}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	runtime, err := NewProxmoxRuntime(ProxmoxConfig{BaseURL: server.URL, Node: "pve-node", Token: bootstrapHostKeyTestToken, Client: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runtime.InstallBootstrapGuestIdentity(context.Background(), binding, publicMaterialFixture(t))
	if !errors.Is(err, ErrBootstrapHostKeyIsolation) || termproxyPosts != 0 {
		t.Fatalf("non-shell console mode error=%v termproxy posts=%d; want rejection before console", err, termproxyPosts)
	}
}

func TestInstallBootstrapGuestIdentityBoundsOutputAndRejectsInvalidClientKey(t *testing.T) {
	t.Run("invalid client key before PVE calls", func(t *testing.T) {
		options := &bootstrapHostKeyConsoleOptions{}
		runtime, server := newGuestIdentityConsoleRuntime(t, options)
		defer server.Close()
		_, err := runtime.InstallBootstrapGuestIdentity(context.Background(), options.binding, "ssh-ed25519 INVALID_PUBLIC_KEY")
		if !errors.Is(err, errBootstrapGuestIdentityCommand) || options.configCalls != 0 || options.consolePosts != 0 {
			t.Fatalf("invalid client key error=%v config=%d console posts=%d", err, options.configCalls, options.consolePosts)
		}
	})
	t.Run("oversized output", func(t *testing.T) {
		options := &bootstrapHostKeyConsoleOptions{}
		runtime, server := newGuestIdentityConsoleRuntime(t, options)
		defer server.Close()
		options.terminal = func(conn *websocket.Conn, _ string) {
			_ = websocket.Message.Send(conn, []byte("OUTPUT_SECRET"+strings.Repeat("x", consoleFrameLimit)))
		}
		_, err := runtime.InstallBootstrapGuestIdentity(context.Background(), options.binding, publicMaterialFixture(t))
		if !errors.Is(err, ErrConsoleOutputLimit) {
			t.Fatalf("oversized output error = %v; want output limit", err)
		}
		if strings.Contains(err.Error(), "OUTPUT_SECRET") {
			t.Fatal("output limit error leaked terminal output")
		}
	})
}

func guestIdentityTranscript(markers [6]string, identity [3]string, hostPublicKey string) string {
	return strings.Join([]string{markers[0], identity[0], markers[1], identity[1], markers[2], identity[2], markers[3], hostPublicKey, markers[4], markers[5]}, "\n") + "\n"
}

func newGuestIdentityConsoleRuntime(t *testing.T, options *bootstrapHostKeyConsoleOptions) (*ProxmoxRuntime, *httptest.Server) {
	t.Helper()
	if options.binding.VMID == 0 {
		options.binding = store.SessionRuntimeBinding{ID: "binding-a", SessionID: "session-a", EpochID: "epoch-a", Generation: "gen-1", VMID: 4002}
	}
	return newBootstrapHostKeyRuntime(t, options)
}

func guestIdentityMarkersFromTermproxyCommand(frame string) ([6]string, bool) {
	var markers [6]string
	if !strings.HasPrefix(frame, "0:") {
		return markers, false
	}
	lengthText, command, ok := strings.Cut(strings.TrimPrefix(frame, "0:"), ":")
	length, err := strconv.Atoi(lengthText)
	if !ok || err != nil || length != len([]byte(command)) || !strings.HasPrefix(command, "printf %s '") || !strings.HasSuffix(command, "' | base64 -d | sh\n") {
		return markers, false
	}
	encoded := strings.TrimSuffix(strings.TrimPrefix(command, "printf %s '"), "' | base64 -d | sh\n")
	script, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil {
		return markers, false
	}
	re := regexp.MustCompile(`(__CODEX_GUESTID_[a-f0-9]{32}_(?:BEGIN|UID|OS|HOST|KEY|END)__)`)
	matches := re.FindAllStringSubmatch(string(script), -1)
	if len(matches) != len(markers) {
		return markers, false
	}
	for i, match := range matches {
		markers[i] = match[1]
		if !validGuestIdentityMarker(markers[i]) {
			return [6]string{}, false
		}
		for j := 0; j < i; j++ {
			if markers[j] == markers[i] {
				return [6]string{}, false
			}
		}
	}
	return markers, true
}
