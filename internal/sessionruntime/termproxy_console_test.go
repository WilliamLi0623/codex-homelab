package sessionruntime

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/WilliamLi0623/codex-homelab/internal/store"
	"golang.org/x/net/websocket"
)

const (
	consoleTestToken  = "PVEAPIToken=test-secret"
	consoleTestTicket = "ticket-secret"
)

type consoleTestOptions struct {
	mode         string
	missingMode  bool
	owner        string
	vmid         int
	unprivileged int
	state        string
	authReply    string
	authSplit    bool
	terminal     func(*websocket.Conn, string)
	apiCalls     int
	postCalls    int
	wsCalls      int
	gotAuth      string
	gotResize    string
	gotCommand   string
}

func consoleTestBinding() store.SessionRuntimeBinding {
	return store.SessionRuntimeBinding{VMID: 4005, SessionID: "session-a", EpochID: "epoch-a", Generation: "gen-1"}
}

func newConsoleTestRuntime(t *testing.T, options *consoleTestOptions) (*ProxmoxRuntime, *httptest.Server) {
	t.Helper()
	if options.mode == "" {
		options.mode = "shell"
	}
	if options.owner == "" {
		var err error
		options.owner, err = encodeOwnership(sessionOwnership{Version: 1, SessionID: "session-a", EpochID: "epoch-a", Generation: "gen-1"})
		if err != nil {
			t.Fatal(err)
		}
	}
	if options.vmid == 0 {
		options.vmid = 4005
	}
	mux := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api2/json/nodes/pve-node/lxc/4005/vncwebsocket" {
			options.wsCalls++
			if r.Header.Get("Authorization") != consoleTestToken {
				t.Errorf("WebSocket Authorization header missing or incorrect")
			}
			if r.URL.Query().Get("port") != "5905" || r.URL.Query().Get("vncticket") != consoleTestTicket {
				t.Errorf("WebSocket query did not contain the issued port and ticket")
			}
			if len(r.Header.Values("Sec-WebSocket-Protocol")) == 0 || !strings.Contains(r.Header.Get("Sec-WebSocket-Protocol"), "binary") {
				t.Errorf("WebSocket did not request the binary subprotocol")
			}
			websocket.Server{Config: websocket.Config{Protocol: []string{"binary"}}, Handshake: func(config *websocket.Config, _ *http.Request) error {
				config.Protocol = []string{"binary"}
				return nil
			}, Handler: websocket.Handler(func(conn *websocket.Conn) {
				var auth []byte
				if err := websocket.Message.Receive(conn, &auth); err != nil {
					return
				}
				options.gotAuth = string(auth)
				reply := options.authReply
				if reply == "" {
					reply = "OK"
				}
				if options.authSplit && reply == "OK" {
					if err := websocket.Message.Send(conn, []byte("O")); err != nil {
						return
					}
					_ = websocket.Message.Send(conn, []byte("K"))
				} else if err := websocket.Message.Send(conn, []byte(reply)); err != nil {
					return
				}
				if reply != "OK" {
					return
				}
				var resize []byte
				if err := websocket.Message.Receive(conn, &resize); err != nil {
					return
				}
				options.gotResize = string(resize)
				if options.gotResize != "1:120:40:" {
					return
				}
				var command []byte
				if err := websocket.Message.Receive(conn, &command); err != nil {
					return
				}
				options.gotCommand = string(command)
				if options.terminal != nil {
					options.terminal(conn, string(command))
				}
			})}.ServeHTTP(w, r)
			return
		}
		options.apiCalls++
		if r.Header.Get("Authorization") != consoleTestToken {
			t.Errorf("Proxmox API Authorization header missing or incorrect")
		}
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/config"):
			config := map[string]any{
				"vmid":         options.vmid,
				"hostname":     sessionHostname("session-a", "epoch-a", "gen-1"),
				"description":  options.owner,
				"unprivileged": options.unprivileged,
				"rootfs":       "local:4005/vm-4005-disk-0.raw,size=8G",
			}
			if !options.missingMode {
				config["cmode"] = options.mode
			}
			data, _ := json.Marshal(map[string]any{"data": config})
			_, _ = w.Write(data)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/status/current"):
			state := options.state
			if state == "" {
				state = "running"
			}
			data, _ := json.Marshal(map[string]any{"data": map[string]any{"vmid": 4005, "status": state}})
			_, _ = w.Write(data)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/termproxy"):
			options.postCalls++
			_, _ = w.Write([]byte(`{"data":{"user":"root@pam","ticket":"ticket-secret","port":5905,"upid":"UPID:node:termproxy"}}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	})
	server := httptest.NewTLSServer(mux)
	client := server.Client()
	runtime, err := NewProxmoxRuntime(ProxmoxConfig{BaseURL: server.URL, Node: "pve-node", Token: consoleTestToken, Client: client})
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	return runtime, server
}

func consoleOutput(conn *websocket.Conn, output string) {
	_ = websocket.Message.Send(conn, []byte(output))
}

func TestProbeConsoleUsesBinaryTermproxyAndReturnsIsolatedIdentity(t *testing.T) {
	options := &consoleTestOptions{authSplit: true}
	options.terminal = func(conn *websocket.Conn, command string) {
		frame := strings.TrimPrefix(command, "0:")
		_, encoded, ok := strings.Cut(frame, ":")
		if !ok {
			t.Errorf("terminal command was not framed: %q", command)
			return
		}
		snippetBytes, err := decodeConsoleSnippetForTest(encoded)
		if err != nil {
			t.Errorf("decode command snippet: %v", err)
			return
		}
		snippet := string(snippetBytes)
		for _, harmless := range []string{"id -u", "uname -s", "hostname"} {
			if !strings.Contains(snippet, harmless) {
				t.Errorf("probe snippet omitted %q", harmless)
			}
		}
		if strings.Contains(command, "__CODEX_PROBE_") {
			t.Errorf("random markers were exposed in terminal input")
		}
		consoleOutput(conn, "echoed terminal input\r\n")
		consoleOutput(conn, markerOutputForTest(snippet, "0", "Linux", sessionHostname("session-a", "epoch-a", "gen-1")))
	}
	runtime, server := newConsoleTestRuntime(t, options)
	defer server.Close()

	result, err := runtime.ProbeConsole(context.Background(), consoleTestBinding())
	if err != nil {
		t.Fatalf("ProbeConsole() error = %v (POSTs=%d, WS requests=%d)", err, options.postCalls, options.wsCalls)
	}
	if result.UID != 0 || result.OS != "Linux" || result.Hostname != sessionHostname("session-a", "epoch-a", "gen-1") {
		t.Fatalf("ProbeConsole() = %+v, want root Linux shell and the bound session hostname", result)
	}
	if options.postCalls != 1 || options.wsCalls != 1 {
		t.Fatalf("termproxy POSTs=%d WebSocket dials=%d, want one each", options.postCalls, options.wsCalls)
	}
	if options.gotAuth != "root@pam:"+consoleTestTicket+"\n" {
		t.Fatalf("termproxy auth frame was not user:ticket newline")
	}
	if options.gotResize != "1:120:40:" {
		t.Fatalf("termproxy resize frame = %q, want 1:120:40: before command", options.gotResize)
	}
	if !strings.HasSuffix(options.gotCommand, "\n") {
		t.Fatalf("termproxy command has no execution newline")
	}
}

func TestProbeConsoleGatesIdentityRangeAndConsoleModeBeforePOST(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*consoleTestOptions, *store.SessionRuntimeBinding)
	}{
		{name: "owner", mutate: func(o *consoleTestOptions, b *store.SessionRuntimeBinding) { b.SessionID = "other" }},
		{name: "range", mutate: func(_ *consoleTestOptions, b *store.SessionRuntimeBinding) { b.VMID = 3010 }},
		{name: "privileged", mutate: func(o *consoleTestOptions, _ *store.SessionRuntimeBinding) { o.unprivileged = 1 }},
		{name: "not-running", mutate: func(o *consoleTestOptions, _ *store.SessionRuntimeBinding) { o.state = "stopped" }},
		{name: "missing-cmode", mutate: func(o *consoleTestOptions, _ *store.SessionRuntimeBinding) { o.missingMode = true }},
		{name: "cmode", mutate: func(o *consoleTestOptions, _ *store.SessionRuntimeBinding) { o.mode = "console" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			options := &consoleTestOptions{}
			runtime, server := newConsoleTestRuntime(t, options)
			defer server.Close()
			binding := consoleTestBinding()
			test.mutate(options, &binding)
			_, err := runtime.ProbeConsole(context.Background(), binding)
			if err == nil {
				t.Fatal("ProbeConsole() unexpectedly succeeded")
			}
			if options.postCalls != 0 {
				t.Fatalf("termproxy POSTs=%d, want zero for %s gate", options.postCalls, test.name)
			}
		})
	}
}

func TestProbeConsoleRejectsUnsupportedHTTPTransportBeforeRequests(t *testing.T) {
	options := &consoleTestOptions{}
	runtime, server := newConsoleTestRuntime(t, options)
	defer server.Close()
	runtime.client.Transport = roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("unexpected transport use")
	})
	_, err := runtime.ProbeConsole(context.Background(), consoleTestBinding())
	if !errors.Is(err, ErrConsoleHandshake) || options.apiCalls != 0 || options.postCalls != 0 {
		t.Fatalf("ProbeConsole() error=%v API requests=%d POSTs=%d; want unsupported-transport rejection before requests", err, options.apiCalls, options.postCalls)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestProbeConsoleRejectsUntrustedWebSocketCertificate(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		websocket.Server{Config: websocket.Config{Protocol: []string{"binary"}}, Handshake: func(*websocket.Config, *http.Request) error { return nil }, Handler: websocket.Handler(func(*websocket.Conn) {})}.ServeHTTP(w, r)
	}))
	defer server.Close()
	transport := &http.Transport{TLSClientConfig: &tls.Config{}}
	runtime := &ProxmoxRuntime{baseURL: server.URL, node: "pve-node", token: consoleTestToken}
	_, err := dialTermproxy(context.Background(), runtime, 4005, termproxySession{User: "root@pam", Ticket: consoleTestTicket, Port: 5905}, transport)
	if err == nil {
		t.Fatal("dialTermproxy() accepted an untrusted server certificate")
	}
}

func TestProbeConsoleAuthFailureDoesNotLeakCredentials(t *testing.T) {
	options := &consoleTestOptions{authReply: "NO"}
	runtime, server := newConsoleTestRuntime(t, options)
	defer server.Close()
	_, err := runtime.ProbeConsole(context.Background(), consoleTestBinding())
	if err == nil {
		t.Fatal("ProbeConsole() succeeded after terminal authentication rejection")
	}
	for _, secret := range []string{consoleTestToken, consoleTestTicket, "ticket=", "vncticket="} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("ProbeConsole() error leaked sensitive value %q", secret)
		}
	}
}

func TestProbeConsoleHandlesFragmentedMarkersAndIgnoresTerminalEcho(t *testing.T) {
	options := &consoleTestOptions{}
	options.terminal = func(conn *websocket.Conn, command string) {
		encoded := strings.TrimPrefix(command, "0:")
		_, encoded, _ = strings.Cut(encoded, ":")
		decoded, err := decodeConsoleSnippetForTest(encoded)
		if err != nil {
			t.Errorf("decode shell snippet: %v", err)
			return
		}
		snippet := markerOutputForTest(string(decoded), "0", "Linux", sessionHostname("session-a", "epoch-a", "gen-1"))
		consoleOutput(conn, command+"\r\n"+snippet[:len(snippet)/2])
		consoleOutput(conn, snippet[len(snippet)/2:])
	}
	runtime, server := newConsoleTestRuntime(t, options)
	defer server.Close()
	result, err := runtime.ProbeConsole(context.Background(), consoleTestBinding())
	if err != nil || result.Hostname != sessionHostname("session-a", "epoch-a", "gen-1") {
		t.Fatalf("ProbeConsole() = %+v, %v; want fragmented marker result", result, err)
	}
}

func TestProbeConsoleRejectsGuestIdentityMismatch(t *testing.T) {
	tests := []struct {
		name string
		uid  string
		os   string
		host string
	}{
		{name: "non-root", uid: "1000", os: "Linux", host: sessionHostname("session-a", "epoch-a", "gen-1")},
		{name: "non-linux", uid: "0", os: "FreeBSD", host: sessionHostname("session-a", "epoch-a", "gen-1")},
		{name: "wrong-host", uid: "0", os: "Linux", host: "unbound-host"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			options := &consoleTestOptions{terminal: func(conn *websocket.Conn, command string) {
				encoded := strings.TrimPrefix(command, "0:")
				_, encoded, _ = strings.Cut(encoded, ":")
				decoded, err := decodeConsoleSnippetForTest(encoded)
				if err != nil {
					t.Errorf("decode shell snippet: %v", err)
					return
				}
				consoleOutput(conn, markerOutputForTest(string(decoded), test.uid, test.os, test.host))
			}}
			runtime, server := newConsoleTestRuntime(t, options)
			defer server.Close()
			_, err := runtime.ProbeConsole(context.Background(), consoleTestBinding())
			if !errors.Is(err, ErrConsoleIdentityMismatch) {
				t.Fatalf("ProbeConsole() error = %v, want guest identity mismatch", err)
			}
		})
	}
}

func TestProbeConsoleClassifiesDisconnectCancellationAndOutputLimit(t *testing.T) {
	t.Run("disconnect", func(t *testing.T) {
		options := &consoleTestOptions{terminal: func(conn *websocket.Conn, _ string) { _ = conn.Close() }}
		runtime, server := newConsoleTestRuntime(t, options)
		defer server.Close()
		_, err := runtime.ProbeConsole(context.Background(), consoleTestBinding())
		if err == nil || !errors.Is(err, ErrConsoleDisconnected) {
			t.Fatalf("ProbeConsole() error = %v, want disconnect classification", err)
		}
	})
	t.Run("cancel", func(t *testing.T) {
		options := &consoleTestOptions{terminal: func(conn *websocket.Conn, _ string) { time.Sleep(200 * time.Millisecond) }}
		runtime, server := newConsoleTestRuntime(t, options)
		defer server.Close()
		ctx, cancel := context.WithCancel(context.Background())
		time.AfterFunc(30*time.Millisecond, cancel)
		_, err := runtime.ProbeConsole(ctx, consoleTestBinding())
		if err == nil || !errors.Is(err, ErrConsoleCanceled) {
			t.Fatalf("ProbeConsole() error = %v, want cancellation classification", err)
		}
	})
	t.Run("output-bound", func(t *testing.T) {
		options := &consoleTestOptions{terminal: func(conn *websocket.Conn, _ string) { consoleOutput(conn, strings.Repeat("x", 64*1024+1)) }}
		runtime, server := newConsoleTestRuntime(t, options)
		defer server.Close()
		_, err := runtime.ProbeConsole(context.Background(), consoleTestBinding())
		if err == nil || !errors.Is(err, ErrConsoleOutputLimit) {
			t.Fatalf("ProbeConsole() error = %v, want output-limit classification", err)
		}
	})
}

func TestProbeConsoleClassifiesMalformedOutput(t *testing.T) {
	options := &consoleTestOptions{terminal: func(conn *websocket.Conn, _ string) {
		consoleOutput(conn, "welcome to shell\nuid=1000(user)\nLinux\nhostname\n")
	}}
	runtime, server := newConsoleTestRuntime(t, options)
	defer server.Close()
	_, err := runtime.ProbeConsole(context.Background(), consoleTestBinding())
	if err == nil || !errors.Is(err, ErrConsoleMalformedOutput) {
		t.Fatalf("ProbeConsole() error = %v, want malformed-output classification", err)
	}
}

func markerOutputForTest(snippet string, values ...string) string {
	var markers []string
	for _, part := range strings.Split(snippet, "'") {
		if strings.HasPrefix(part, "__CODEX_PROBE_") {
			markers = append(markers, part)
		}
	}
	if len(markers) != 4 || len(values) != 3 {
		return ""
	}
	return strings.Join([]string{markers[0], values[0], markers[1], values[1], markers[2], values[2], markers[3], ""}, "\n")
}

func decodeConsoleSnippetForTest(command string) ([]byte, error) {
	command = strings.TrimSpace(command)
	start := strings.IndexByte(command, '\'')
	if start < 0 {
		return nil, errors.New("encoded shell snippet is missing its opening quote")
	}
	end := strings.IndexByte(command[start+1:], '\'')
	if end < 0 {
		return nil, errors.New("encoded shell snippet is missing its closing quote")
	}
	return base64.StdEncoding.DecodeString(command[start+1 : start+1+end])
}

func TestParseConsoleProbeOutputAllowsUTF8RuneSplitAcrossMessages(t *testing.T) {
	markers := [4]string{"BEGIN", "OS", "HOST", "END"}
	first := []byte("BEGIN\n0\nOS\nLin")
	first = append(first, 0xc3)
	if _, complete, err := parseConsoleProbeOutput(first, markers); err != nil || complete {
		t.Fatalf("partial UTF-8 tail parse = complete %v, err %v; want incomplete without error", complete, err)
	}
	second := append([]byte{0xa9}, []byte("ux\nHOST\nsession\nEND\n")...)
	combined := append(first, second...)
	result, complete, err := parseConsoleProbeOutput(combined, markers)
	if err != nil || !complete || result.OS != "Linéux" || result.Hostname != "session" {
		t.Fatalf("fragmented UTF-8 parse = %+v, complete %v, err %v", result, complete, err)
	}
}

func TestParseConsoleProbeOutputNormalizesRepeatedCROnly(t *testing.T) {
	markers := [4]string{"BEGIN", "OS", "HOST", "END"}
	output := []byte("shell banner\r\nBEGIN\r\r\n0\r\nOS\r\nLinux\r\nHOST\r\nsession\r\nEND\r\r\r\n")
	result, complete, err := parseConsoleProbeOutput(output, markers)
	if err != nil || !complete || result.UID != 0 || result.OS != "Linux" || result.Hostname != "session" {
		t.Fatalf("repeated-CR output parse = %+v, complete %v, err %v", result, complete, err)
	}

	spoofed := []byte(" BEGIN \r\n0\r\nOS\r\nLinux\r\nHOST\r\nsession\r\nEND\r\n")
	_, complete, err = parseConsoleProbeOutput(spoofed, markers)
	if err != nil || complete {
		t.Fatalf("parser normalized marker whitespace: complete %v, err %v", complete, err)
	}
}
