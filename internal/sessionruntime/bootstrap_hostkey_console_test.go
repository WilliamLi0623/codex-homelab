package sessionruntime

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/WilliamLi0623/codex-homelab/internal/store"
	"golang.org/x/net/websocket"
)

const bootstrapHostKeyTestToken = "PVEAPIToken=hostkey-test-secret"

type bootstrapHostKeyConsoleOptions struct {
	binding                  store.SessionRuntimeBinding
	state                    string
	publicKey                string
	identity                 [3]string
	unfenced                 bool
	loseFenceAfterConsole    bool
	changeConfigAfterConsole bool
	terminal                 func(*websocket.Conn, string)
	configCalls              int
	consolePosts             int
	websocketHits            int
	output                   string
}

func newBootstrapHostKeyRuntime(t *testing.T, options *bootstrapHostKeyConsoleOptions) (*ProxmoxRuntime, *httptest.Server) {
	t.Helper()
	if options.binding.VMID == 0 {
		options.binding = consoleTestBinding()
	}
	if options.publicKey == "" {
		options.publicKey = publicMaterialFixture(t)
	}
	if options.identity == [3]string{} {
		options.identity = [3]string{"0", "Linux", sessionHostname(options.binding.SessionID, options.binding.EpochID, options.binding.Generation)}
	}
	if options.state == "" {
		options.state = "running"
	}
	owner, err := encodeOwnership(sessionOwnership{Version: 1, SessionID: options.binding.SessionID, EpochID: options.binding.EpochID, Generation: options.binding.Generation})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != bootstrapHostKeyTestToken {
			t.Errorf("missing PVE authentication")
		}
		if strings.HasSuffix(r.URL.Path, "/vncwebsocket") {
			options.websocketHits++
			websocket.Server{Config: websocket.Config{Protocol: []string{"binary"}}, Handshake: func(config *websocket.Config, _ *http.Request) error {
				config.Protocol = []string{"binary"}
				return nil
			}, Handler: websocket.Handler(func(conn *websocket.Conn) {
				var auth []byte
				if websocket.Message.Receive(conn, &auth) != nil || string(auth) != "root@pam:ticket-secret\n" {
					return
				}
				if websocket.Message.Send(conn, []byte("OK")) != nil {
					return
				}
				var resize []byte
				if websocket.Message.Receive(conn, &resize) != nil || string(resize) != "1:120:40:" {
					return
				}
				var command []byte
				if websocket.Message.Receive(conn, &command) != nil {
					return
				}
				options.output = string(command)
				if options.terminal != nil {
					options.terminal(conn, string(command))
					return
				}
				markers, ok := hostKeyMarkersFromCommand(string(command))
				if !ok {
					t.Errorf("command did not contain six random markers")
					return
				}
				lines := []string{markers[0], options.identity[0], markers[1], options.identity[1], markers[2], options.identity[2], markers[3], options.publicKey, markers[4], markers[5]}
				_ = websocket.Message.Send(conn, []byte(strings.Join(lines, "\n")+"\n"))
			})}.ServeHTTP(w, r)
			return
		}
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/config"):
			options.configCalls++
			network := "name=eth0,bridge=vmbr0,ip=dhcp,link_down=1"
			if options.unfenced || options.configCalls > 1 && options.loseFenceAfterConsole {
				network = "name=eth0,bridge=vmbr0,ip=dhcp,link_down=0"
			} else if options.configCalls > 1 && options.changeConfigAfterConsole {
				network = "name=eth0,bridge=vmbr1,ip=dhcp,link_down=1"
			}
			data := map[string]any{"vmid": options.binding.VMID, "hostname": sessionHostname(options.binding.SessionID, options.binding.EpochID, options.binding.Generation), "description": owner, "unprivileged": 0, "onboot": 0, "cmode": "shell", "rootfs": "local:4005/vm-4005-disk-0.raw,size=8G", "mp0": "pool:subvol-4005-disk-1,mp=/workspace", "net0": network}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/status/current"):
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"vmid": options.binding.VMID, "status": options.state}})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/termproxy"):
			options.consolePosts++
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"user": "root@pam", "ticket": "ticket-secret", "port": 5905, "upid": "UPID:termproxy"}})
		default:
			t.Errorf("unexpected API request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	})
	server := httptest.NewTLSServer(mux)
	runtime, err := NewProxmoxRuntime(ProxmoxConfig{BaseURL: server.URL, Node: "pve-node", Token: bootstrapHostKeyTestToken, Client: server.Client()})
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	return runtime, server
}

func hostKeyMarkersFromCommand(command string) ([6]string, bool) {
	var markers [6]string
	frame := strings.TrimPrefix(command, "0:")
	_, encoded, ok := strings.Cut(frame, ":")
	if !ok {
		return markers, false
	}
	start, end := strings.IndexByte(encoded, '\''), strings.LastIndexByte(encoded, '\'')
	if start < 0 || end <= start {
		return markers, false
	}
	script, err := base64.StdEncoding.DecodeString(encoded[start+1 : end])
	if err != nil {
		return markers, false
	}
	re := regexp.MustCompile(`'(__CODEX_HOSTKEY_[A-Za-z0-9_-]+__)'`)
	matches := re.FindAllStringSubmatch(string(script), -1)
	if len(matches) != len(markers) {
		return markers, false
	}
	for i, match := range matches {
		markers[i] = match[1]
	}
	return markers, true
}

func TestProbeBootstrapHostKeyReadsFixedPublicKeyAndReturnsIdentityProof(t *testing.T) {
	options := &bootstrapHostKeyConsoleOptions{}
	runtime, server := newBootstrapHostKeyRuntime(t, options)
	defer server.Close()

	result, err := runtime.ProbeBootstrapHostKey(context.Background(), options.binding)
	if err != nil {
		t.Fatalf("ProbeBootstrapHostKey() error = %v", err)
	}
	if result.PublicKey != options.publicKey || result.UID != 0 || result.OS != "Linux" || result.Hostname != sessionHostname(options.binding.SessionID, options.binding.EpochID, options.binding.Generation) {
		t.Fatalf("ProbeBootstrapHostKey() = %+v; public key or identity proof mismatch", result)
	}
	if !validBootstrapEvidence(store.SessionBootstrapEvidence{SHA256: result.IsolationSHA256}) {
		t.Fatalf("ProbeBootstrapHostKey() returned invalid isolation proof %q", result.IsolationSHA256)
	}
	if options.configCalls != 2 || options.consolePosts != 1 || options.websocketHits != 1 {
		t.Fatalf("config reads=%d termproxy POSTs=%d WebSocket dials=%d; want isolation recheck around one console read", options.configCalls, options.consolePosts, options.websocketHits)
	}
	commandFrame := strings.TrimPrefix(options.output, "0:")
	_, encoded, _ := strings.Cut(commandFrame, ":")
	start, end := strings.IndexByte(encoded, '\''), strings.LastIndexByte(encoded, '\'')
	script, decodeErr := base64.StdEncoding.DecodeString(encoded[start+1 : end])
	if decodeErr != nil || !strings.Contains(string(script), "cat -- /etc/ssh/ssh_host_p28_ed25519_key.pub") {
		t.Fatalf("probe did not read the fixed production public-key path")
	}
	if strings.Contains(string(script), "private") || strings.Contains(string(script), "ssh_host_p28_ed25519_key'") {
		t.Fatalf("probe command targeted possible private material")
	}
}

func TestProbeBootstrapHostKeyNormalizesStandardPublicKeyComment(t *testing.T) {
	baseKey := publicMaterialFixture(t)
	options := &bootstrapHostKeyConsoleOptions{publicKey: baseKey + " root@p28-session host key"}
	runtime, server := newBootstrapHostKeyRuntime(t, options)
	defer server.Close()

	result, err := runtime.ProbeBootstrapHostKey(context.Background(), options.binding)
	if err != nil {
		t.Fatalf("ProbeBootstrapHostKey() error = %v", err)
	}
	if result.PublicKey != baseKey || !validMaterialPublicKey(result.PublicKey) || len(strings.Fields(result.PublicKey)) != 2 {
		t.Fatalf("commented OpenSSH public key was not safely normalized: %q", result.PublicKey)
	}
}

func TestBootstrapHostKeyNormalizerRejectsOptionsAndControlCharacters(t *testing.T) {
	key := publicMaterialFixture(t)
	for _, line := range []string{"from=192.0.2.1 " + key, key + "\tcomment", key + " comment\x7f"} {
		if normalized, ok := normalizeBootstrapHostPublicKey(line); ok {
			t.Errorf("accepted unsafe public-key line %q as %q", line, normalized)
		}
	}
}

func TestProbeBootstrapHostKeyRejectsUnfencedGuestBeforeConsole(t *testing.T) {
	options := &bootstrapHostKeyConsoleOptions{unfenced: true}
	runtime, server := newBootstrapHostKeyRuntime(t, options)
	defer server.Close()
	_, err := runtime.ProbeBootstrapHostKey(context.Background(), options.binding)
	if !errors.Is(err, ErrBootstrapHostKeyIsolation) || options.consolePosts != 0 || options.websocketHits != 0 {
		t.Fatalf("unfenced/stopped guest result error=%v posts=%d websocket=%d; expected rejection before console", err, options.consolePosts, options.websocketHits)
	}
}

func TestProbeBootstrapHostKeyRequiresRunningGuestBeforeConsole(t *testing.T) {
	options := &bootstrapHostKeyConsoleOptions{state: "stopped"}
	runtime, server := newBootstrapHostKeyRuntime(t, options)
	defer server.Close()
	_, err := runtime.ProbeBootstrapHostKey(context.Background(), options.binding)
	if !errors.Is(err, ErrConsoleNotRunning) || options.consolePosts != 0 || options.websocketHits != 0 {
		t.Fatalf("stopped guest error=%v posts=%d websocket=%d; expected rejection before console", err, options.consolePosts, options.websocketHits)
	}
}

func TestProbeBootstrapHostKeyRejectsWrongIdentityAndMalformedKey(t *testing.T) {
	for _, tc := range []struct {
		name     string
		identity [3]string
		key      string
	}{
		{name: "wrong hostname", identity: [3]string{"0", "Linux", "unbound-host"}},
		{name: "non-root", identity: [3]string{"1000", "Linux", sessionHostname("session-a", "epoch-a", "gen-1")}},
		{name: "non-linux", identity: [3]string{"0", "FreeBSD", sessionHostname("session-a", "epoch-a", "gen-1")}},
		{name: "malformed key", key: "ssh-ed25519 SECRET_OUTPUT_NOT_A_KEY"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			options := &bootstrapHostKeyConsoleOptions{identity: tc.identity, publicKey: tc.key}
			runtime, server := newBootstrapHostKeyRuntime(t, options)
			defer server.Close()
			_, err := runtime.ProbeBootstrapHostKey(context.Background(), options.binding)
			want := ErrConsoleIdentityMismatch
			if tc.name == "malformed key" {
				want = ErrConsoleMalformedOutput
			}
			if !errors.Is(err, want) {
				t.Fatalf("invalid identity or key error=%v; want %v", err, want)
			}
			for _, secret := range []string{bootstrapHostKeyTestToken, "ticket-secret", "SECRET_OUTPUT_NOT_A_KEY", "private key"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("error leaked sensitive value %q", secret)
				}
			}
		})
	}
}

func TestProbeBootstrapHostKeyRechecksIsolationAfterConsole(t *testing.T) {
	options := &bootstrapHostKeyConsoleOptions{loseFenceAfterConsole: true}
	runtime, server := newBootstrapHostKeyRuntime(t, options)
	defer server.Close()
	_, err := runtime.ProbeBootstrapHostKey(context.Background(), options.binding)
	if !errors.Is(err, ErrBootstrapHostKeyIsolation) || options.configCalls != 2 {
		t.Fatalf("post-console isolation recheck error=%v config reads=%d; expected failure on changed fence", err, options.configCalls)
	}
}

func TestProbeBootstrapHostKeyRejectsIsolationConfigMutation(t *testing.T) {
	options := &bootstrapHostKeyConsoleOptions{changeConfigAfterConsole: true}
	runtime, server := newBootstrapHostKeyRuntime(t, options)
	defer server.Close()
	_, err := runtime.ProbeBootstrapHostKey(context.Background(), options.binding)
	if !errors.Is(err, ErrBootstrapHostKeyIsolation) || options.configCalls != 2 {
		t.Fatalf("changed isolation snapshot error=%v config reads=%d; expected digest mismatch rejection", err, options.configCalls)
	}
}

func TestProbeBootstrapHostKeyRejectsMultiplePublicKeys(t *testing.T) {
	options := &bootstrapHostKeyConsoleOptions{}
	runtime, server := newBootstrapHostKeyRuntime(t, options)
	defer server.Close()
	options.terminal = func(conn *websocket.Conn, command string) {
		markers, ok := hostKeyMarkersFromCommand(command)
		if !ok {
			return
		}
		lines := []string{markers[0], "0", markers[1], "Linux", markers[2], sessionHostname(options.binding.SessionID, options.binding.EpochID, options.binding.Generation), markers[3], options.publicKey, options.publicKey, markers[4], markers[5]}
		_ = websocket.Message.Send(conn, []byte(strings.Join(lines, "\n")+"\n"))
	}
	_, err := runtime.ProbeBootstrapHostKey(context.Background(), options.binding)
	if !errors.Is(err, ErrConsoleMalformedOutput) {
		t.Fatalf("multiple-key console output error=%v; want malformed-output rejection", err)
	}
}

func TestProbeBootstrapHostKeyClassifiesDisconnectLimitCancellationAndRedacts(t *testing.T) {
	for _, tc := range []struct {
		name     string
		terminal func(*websocket.Conn, string)
		cancel   bool
		want     error
	}{
		{name: "disconnect", terminal: func(conn *websocket.Conn, _ string) { _ = conn.Close() }, want: ErrConsoleDisconnected},
		{name: "limit", terminal: func(conn *websocket.Conn, _ string) {
			_ = websocket.Message.Send(conn, []byte("OUTPUT_SECRET"+strings.Repeat("x", consoleFrameLimit)))
		}, want: ErrConsoleOutputLimit},
		{name: "cancel", terminal: func(_ *websocket.Conn, _ string) { time.Sleep(150 * time.Millisecond) }, cancel: true, want: ErrConsoleCanceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			options := &bootstrapHostKeyConsoleOptions{terminal: tc.terminal}
			runtime, server := newBootstrapHostKeyRuntime(t, options)
			defer server.Close()
			ctx := context.Background()
			cancel := func() {}
			if tc.cancel {
				var cancelCtx context.CancelFunc
				ctx, cancelCtx = context.WithTimeout(ctx, 30*time.Millisecond)
				cancel = cancelCtx
			}
			defer cancel()
			_, err := runtime.ProbeBootstrapHostKey(ctx, options.binding)
			if !errors.Is(err, tc.want) {
				t.Fatalf("ProbeBootstrapHostKey() error=%v; want %v", err, tc.want)
			}
			for _, secret := range []string{bootstrapHostKeyTestToken, "ticket-secret", "OUTPUT_SECRET", "vncticket="} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("error leaked sensitive value %q", secret)
				}
			}
		})
	}
}

func TestBootstrapHostKeyPublicKeyFixtureMatchesStrictParser(t *testing.T) {
	key := publicMaterialFixture(t)
	parts := strings.Split(key, " ")
	blob, err := base64.StdEncoding.DecodeString(parts[1])
	if err != nil || len(blob) != 51 || binary.BigEndian.Uint32(blob[:4]) != 11 {
		t.Fatalf("test key fixture is invalid: %v", err)
	}
	if !validMaterialPublicKey(key) {
		t.Fatal("generated fixture failed existing strict Ed25519 public-key validator")
	}
}
