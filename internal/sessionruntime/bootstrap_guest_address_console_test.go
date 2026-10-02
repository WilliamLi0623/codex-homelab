package sessionruntime

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/WilliamLi0623/codex-homelab/internal/store"
	"golang.org/x/net/websocket"
)

const bootstrapGuestAddressTestToken = "PVEAPIToken=guest-address-test-secret"

type bootstrapGuestAddressConsoleOptions struct {
	binding       store.SessionRuntimeBinding
	state         string
	network       string
	digests       []string
	terminal      func(*websocket.Conn, [3]string)
	configCalls   int
	consolePosts  int
	websocketHits int
}

func newBootstrapGuestAddressRuntime(t *testing.T, options *bootstrapGuestAddressConsoleOptions) (*ProxmoxRuntime, *httptest.Server) {
	t.Helper()
	if options.binding.VMID == 0 {
		options.binding = consoleTestBinding()
	}
	if options.binding.ID == "" {
		options.binding.ID = "binding-a"
	}
	if options.state == "" {
		options.state = "running"
	}
	if options.network == "" {
		options.network = "name=eth0,bridge=vmbr0,ip=dhcp,link_down=0,tag=20"
	}
	if len(options.digests) == 0 {
		options.digests = []string{strings.Repeat("a", 40)}
	}
	owner, err := encodeOwnership(sessionOwnership{Version: 1, SessionID: options.binding.SessionID, EpochID: options.binding.EpochID, Generation: options.binding.Generation})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != bootstrapGuestAddressTestToken {
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
				markers, ok := bootstrapGuestAddressMarkersFromCommand(string(command))
				if !ok {
					t.Errorf("command did not contain three nonce-bound address markers")
					return
				}
				if options.terminal != nil {
					options.terminal(conn, markers)
					return
				}
				lines := []string{markers[0], "10.58.2.231/24", markers[1], markers[2]}
				_ = websocket.Message.Send(conn, []byte(strings.Join(lines, "\n")+"\n"))
			})}.ServeHTTP(w, r)
			return
		}
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/config"):
			index := options.configCalls
			options.configCalls++
			if index >= len(options.digests) {
				index = len(options.digests) - 1
			}
			data := map[string]any{
				"vmid": options.binding.VMID, "hostname": sessionHostname(options.binding.SessionID, options.binding.EpochID, options.binding.Generation),
				"description": owner, "unprivileged": 0, "onboot": 0, "cmode": "shell",
				"rootfs": "local:subvol-4005-disk-0,size=8G", "mp0": "pool:subvol-4005-disk-1,mp=/workspace",
				"net0": options.network, "digest": options.digests[index],
			}
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
	runtime, err := NewProxmoxRuntime(ProxmoxConfig{BaseURL: server.URL, Node: "pve-node", Token: bootstrapGuestAddressTestToken, Client: server.Client()})
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	return runtime, server
}

func bootstrapGuestAddressMarkersFromCommand(command string) ([3]string, bool) {
	var markers [3]string
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
	pattern := regexp.MustCompile(`(__CODEX_GUESTADDR_[0-9a-f]{32}_(?:BEGIN|IP|END)__)`)
	matches := pattern.FindAllString(string(script), -1)
	if len(matches) != len(markers) {
		return markers, false
	}
	copy(markers[:], matches)
	return markers, true
}

func TestProbeBootstrapGuestIPv4ReadsOneOwnedEnabledDHCPInterfaceAndRechecksConfig(t *testing.T) {
	options := &bootstrapGuestAddressConsoleOptions{digests: []string{strings.Repeat("a", 40), strings.Repeat("a", 40)}}
	runtime, server := newBootstrapGuestAddressRuntime(t, options)
	defer server.Close()

	result, err := runtime.ProbeBootstrapGuestIPv4(context.Background(), options.binding)
	if err != nil {
		t.Fatalf("ProbeBootstrapGuestIPv4() error = %v", err)
	}
	if got := result.Address.String(); got != "10.58.2.231" {
		t.Fatalf("ProbeBootstrapGuestIPv4() address = %q, want 10.58.2.231", got)
	}
	if !validBootstrapEvidence(store.SessionBootstrapEvidence{SHA256: result.ConfigSHA256}) {
		t.Fatalf("invalid config evidence %q", result.ConfigSHA256)
	}
	if options.configCalls != 2 || options.consolePosts != 1 || options.websocketHits != 1 {
		t.Fatalf("config reads=%d termproxy POSTs=%d websocket dials=%d; want two config reads around one console probe", options.configCalls, options.consolePosts, options.websocketHits)
	}
}

func TestProbeBootstrapGuestIPv4RejectsChangedConfigAfterConsole(t *testing.T) {
	options := &bootstrapGuestAddressConsoleOptions{digests: []string{strings.Repeat("a", 40), strings.Repeat("b", 40)}}
	runtime, server := newBootstrapGuestAddressRuntime(t, options)
	defer server.Close()
	if _, err := runtime.ProbeBootstrapGuestIPv4(context.Background(), options.binding); !errors.Is(err, ErrBootstrapGuestAddress) {
		t.Fatalf("changed PVE configuration error = %v, want ErrBootstrapGuestAddress", err)
	}
	if options.configCalls != 2 {
		t.Fatalf("config reads=%d, want before/after verification", options.configCalls)
	}
}

func TestProbeBootstrapGuestIPv4RejectsWrongStateOrNetworkBeforeOpeningConsole(t *testing.T) {
	for _, tc := range []struct {
		name    string
		state   string
		network string
	}{
		{name: "stopped", state: "stopped"},
		{name: "wrong interface", network: "name=eth1,bridge=vmbr0,ip=dhcp,link_down=0"},
		{name: "wrong bridge", network: "name=eth0,bridge=vmbr1,ip=dhcp,link_down=0"},
		{name: "network still fenced", network: "name=eth0,bridge=vmbr0,ip=dhcp,link_down=1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			options := &bootstrapGuestAddressConsoleOptions{state: tc.state, network: tc.network}
			runtime, server := newBootstrapGuestAddressRuntime(t, options)
			defer server.Close()
			if _, err := runtime.ProbeBootstrapGuestIPv4(context.Background(), options.binding); !errors.Is(err, ErrBootstrapGuestAddress) {
				t.Fatalf("ProbeBootstrapGuestIPv4() error = %v, want ErrBootstrapGuestAddress", err)
			}
			if options.consolePosts != 0 || options.websocketHits != 0 {
				t.Fatalf("invalid guest reached console: posts=%d websockets=%d", options.consolePosts, options.websocketHits)
			}
		})
	}
}

func TestProbeBootstrapGuestIPv4RejectsMalformedAndMultipleAddresses(t *testing.T) {
	for _, value := range []string{"198.51.100.2/24", "127.0.0.1/8", "10.58.2.231/24\n10.58.2.232/24", "not-an-ip/24"} {
		t.Run(strings.ReplaceAll(value, "\n", "_"), func(t *testing.T) {
			options := &bootstrapGuestAddressConsoleOptions{}
			options.terminal = func(conn *websocket.Conn, markers [3]string) {
				lines := []string{markers[0], markers[1], value, markers[2]}
				_ = websocket.Message.Send(conn, []byte(strings.Join(lines, "\n")+"\n"))
			}
			runtime, server := newBootstrapGuestAddressRuntime(t, options)
			defer server.Close()
			if _, err := runtime.ProbeBootstrapGuestIPv4(context.Background(), options.binding); !errors.Is(err, ErrBootstrapGuestAddress) {
				t.Fatalf("invalid guest address error = %v, want ErrBootstrapGuestAddress", err)
			}
		})
	}
}
