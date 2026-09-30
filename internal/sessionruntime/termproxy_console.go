package sessionruntime

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/WilliamLi0623/codex-homelab/internal/store"
	"golang.org/x/net/websocket"
)

const (
	consoleProbeDeadline = 30 * time.Second
	consoleOutputLimit   = 64 * 1024
	consoleFrameLimit    = 64 * 1024
	consoleProbeColumns  = 120
	consoleProbeRows     = 40
)

var (
	ErrConsoleOwnership        = errors.New("console probe ownership verification failed")
	ErrConsoleNotRunning       = errors.New("console probe requires a running guest")
	ErrConsoleMode             = errors.New("console probe requires cmode=shell")
	ErrConsoleHandshake        = errors.New("console probe termproxy handshake failed")
	ErrConsoleMalformedOutput  = errors.New("console probe returned malformed output")
	ErrConsoleIdentityMismatch = errors.New("console probe guest identity does not match the Session binding")
	ErrConsoleOutputLimit      = errors.New("console probe output exceeded its limit")
	ErrConsoleCanceled         = errors.New("console probe was canceled or timed out")
	ErrConsoleDisconnected     = errors.New("console probe terminal disconnected")
)

type ConsoleProbeResult struct {
	UID      int
	OS       string
	Hostname string
}

type termproxySession struct {
	User   string `json:"user"`
	Ticket string `json:"ticket"`
	Port   int    `json:"port"`
	UPID   string `json:"upid"`
}

func (session *termproxySession) UnmarshalJSON(data []byte) error {
	var decoded struct {
		User   string          `json:"user"`
		Ticket string          `json:"ticket"`
		Port   json.RawMessage `json:"port"`
		UPID   string          `json:"upid"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		return errors.New("invalid termproxy response")
	}
	port, err := parseTermproxyPort(decoded.Port)
	if err != nil {
		return err
	}
	session.User = decoded.User
	session.Ticket = decoded.Ticket
	session.Port = port
	session.UPID = decoded.UPID
	return nil
}

func parseTermproxyPort(raw json.RawMessage) (int, error) {
	invalid := errors.New("termproxy port must be decimal and in range 5900-5999")
	value := bytes.TrimSpace(raw)
	if len(value) == 0 {
		return 0, invalid
	}
	if value[0] == '"' {
		var text string
		if err := json.Unmarshal(value, &text); err != nil {
			return 0, invalid
		}
		value = []byte(text)
	}
	if len(value) == 0 {
		return 0, invalid
	}
	for _, digit := range value {
		if digit < '0' || digit > '9' {
			return 0, invalid
		}
	}
	port, err := strconv.Atoi(string(value))
	if err != nil || port < 5900 || port > 5999 {
		return 0, invalid
	}
	return port, nil
}

// ProbeConsole performs a fixed, read-only guest identity probe. It does not
// expose an arbitrary command channel and is intentionally not used by runtime
// lifecycle operations.
func (r *ProxmoxRuntime) ProbeConsole(ctx context.Context, binding store.SessionRuntimeBinding) (ConsoleProbeResult, error) {
	var empty ConsoleProbeResult
	if ctx == nil {
		return empty, fmt.Errorf("%w: missing context", ErrConsoleCanceled)
	}
	probeCtx, cancel := context.WithTimeout(ctx, consoleProbeDeadline)
	defer cancel()

	transport, err := consoleHTTPTransport(r.client)
	if err != nil {
		return empty, fmt.Errorf("%w: unsupported HTTP transport", ErrConsoleHandshake)
	}
	if err := probeCtx.Err(); err != nil {
		return empty, fmt.Errorf("%w: %v", ErrConsoleCanceled, err)
	}
	if err := r.VerifyIdentity(probeCtx, binding); err != nil {
		if probeCtx.Err() != nil {
			return empty, fmt.Errorf("%w: %v", ErrConsoleCanceled, probeCtx.Err())
		}
		return empty, ErrConsoleOwnership
	}
	state, err := r.Observe(probeCtx, binding.VMID)
	if err != nil {
		if probeCtx.Err() != nil {
			return empty, fmt.Errorf("%w: %v", ErrConsoleCanceled, probeCtx.Err())
		}
		return empty, fmt.Errorf("%w: guest state could not be verified", ErrConsoleNotRunning)
	}
	if state != RuntimeRunning {
		return empty, ErrConsoleNotRunning
	}
	config, err := r.containerConfig(probeCtx, binding.VMID)
	if err != nil {
		if probeCtx.Err() != nil {
			return empty, fmt.Errorf("%w: %v", ErrConsoleCanceled, probeCtx.Err())
		}
		return empty, ErrConsoleMode
	}
	if config["cmode"] != "shell" {
		return empty, ErrConsoleMode
	}
	if err := probeCtx.Err(); err != nil {
		return empty, fmt.Errorf("%w: %v", ErrConsoleCanceled, err)
	}

	path := "/nodes/" + url.PathEscape(r.node) + "/lxc/" + strconv.Itoa(binding.VMID) + "/termproxy"
	var envelope struct {
		Data termproxySession `json:"data"`
	}
	if err := r.request(probeCtx, http.MethodPost, path, nil, &envelope); err != nil {
		if probeCtx.Err() != nil {
			return empty, fmt.Errorf("%w: %v", ErrConsoleCanceled, probeCtx.Err())
		}
		return empty, ErrConsoleHandshake
	}
	session := envelope.Data
	if session.User == "" || session.Ticket == "" || session.UPID == "" || session.Port < 5900 || session.Port > 5999 {
		return empty, ErrConsoleHandshake
	}

	conn, err := dialTermproxy(probeCtx, r, binding.VMID, session, transport)
	if err != nil {
		if probeCtx.Err() != nil {
			return empty, fmt.Errorf("%w: %v", ErrConsoleCanceled, probeCtx.Err())
		}
		return empty, fmt.Errorf("%w: websocket dial", ErrConsoleHandshake)
	}
	defer conn.Close()
	conn.MaxPayloadBytes = consoleFrameLimit

	closed := make(chan struct{})
	go func() {
		select {
		case <-probeCtx.Done():
			_ = conn.Close()
		case <-closed:
		}
	}()
	defer close(closed)

	if err := websocket.Message.Send(conn, []byte(session.User+":"+session.Ticket+"\n")); err != nil {
		return empty, classifyConsoleSocketError(probeCtx, err)
	}
	var authReply [2]byte
	if _, err := io.ReadFull(conn, authReply[:]); err != nil {
		return empty, classifyConsoleSocketError(probeCtx, err)
	}
	if string(authReply[:]) != "OK" {
		return empty, fmt.Errorf("%w: terminal authentication rejected", ErrConsoleHandshake)
	}

	markers, command, err := makeConsoleProbeCommand()
	if err != nil {
		return empty, ErrConsoleMalformedOutput
	}
	resizeFrame, err := encodeTermproxyResize(consoleProbeColumns, consoleProbeRows)
	if err != nil || websocket.Message.Send(conn, resizeFrame) != nil {
		if probeCtx.Err() != nil {
			return empty, fmt.Errorf("%w: %v", ErrConsoleCanceled, probeCtx.Err())
		}
		return empty, ErrConsoleDisconnected
	}
	inputFrame, err := encodeTermproxyInput(command)
	if err != nil || websocket.Message.Send(conn, inputFrame) != nil {
		if probeCtx.Err() != nil {
			return empty, fmt.Errorf("%w: %v", ErrConsoleCanceled, probeCtx.Err())
		}
		return empty, ErrConsoleDisconnected
	}

	output := make([]byte, 0, 1024)
	parser := consoleProbeOutputParser{markers: markers}
	for {
		var message []byte
		if err := websocket.Message.Receive(conn, &message); err != nil {
			if errors.Is(err, websocket.ErrFrameTooLarge) {
				return empty, ErrConsoleOutputLimit
			}
			if len(output) > 0 && probeCtx.Err() == nil {
				return empty, ErrConsoleMalformedOutput
			}
			return empty, classifyConsoleSocketError(probeCtx, err)
		}
		if len(message) > consoleFrameLimit || len(output)+len(message) > consoleOutputLimit {
			return empty, ErrConsoleOutputLimit
		}
		output = append(output, message...)
		result, complete, parseErr := parser.consume(message)
		if parseErr != nil {
			return empty, ErrConsoleMalformedOutput
		}
		if complete {
			expectedHostname := sessionHostname(binding.SessionID, binding.EpochID, binding.Generation)
			if result.UID != 0 || result.OS != "Linux" || result.Hostname != expectedHostname {
				return empty, ErrConsoleIdentityMismatch
			}
			return result, nil
		}
	}
}

func consoleHTTPTransport(client *http.Client) (*http.Transport, error) {
	if client == nil {
		return nil, errors.New("missing HTTP client")
	}
	transport := client.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	value, ok := transport.(*http.Transport)
	if !ok {
		return nil, errors.New("unsupported HTTP transport")
	}
	return value, nil
}

func dialTermproxy(ctx context.Context, runtime *ProxmoxRuntime, vmid int, session termproxySession, transport *http.Transport) (*websocket.Conn, error) {
	endpoint, err := url.Parse(runtime.baseURL)
	if err != nil {
		return nil, err
	}
	switch endpoint.Scheme {
	case "https":
		endpoint.Scheme = "wss"
	case "http":
		endpoint.Scheme = "ws"
	default:
		return nil, errors.New("unsupported Proxmox URL scheme")
	}
	endpoint.Path = "/api2/json/nodes/" + url.PathEscape(runtime.node) + "/lxc/" + strconv.Itoa(vmid) + "/vncwebsocket"
	query := endpoint.Query()
	query.Set("port", strconv.Itoa(session.Port))
	query.Set("vncticket", session.Ticket)
	endpoint.RawQuery = query.Encode()

	var tlsConfig *tls.Config
	if transport.TLSClientConfig != nil {
		tlsConfig = transport.TLSClientConfig.Clone()
	}
	header := make(http.Header)
	header.Set("Authorization", runtime.token)
	config, err := websocket.NewConfig(endpoint.String(), runtime.baseURL)
	if err != nil {
		return nil, err
	}
	config.Protocol = []string{"binary"}
	config.TlsConfig = tlsConfig
	config.Header = header
	config.Dialer = &net.Dialer{Timeout: consoleProbeDeadline, KeepAlive: 30 * time.Second}
	return config.DialContext(ctx)
}

func makeConsoleProbeCommand() ([4]string, string, error) {
	var markers [4]string
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return markers, "", err
	}
	base := "__CODEX_PROBE_" + base64.RawURLEncoding.EncodeToString(nonce)
	markers = [4]string{base + "_BEGIN__", base + "_OS__", base + "_HOST__", base + "_END__"}
	script := "printf '%s\\n' '" + markers[0] + "'; id -u; printf '%s\\n' '" + markers[1] + "'; uname -s; printf '%s\\n' '" + markers[2] + "'; hostname; printf '%s\\n' '" + markers[3] + "'"
	encoded := base64.StdEncoding.EncodeToString([]byte(script))
	return markers, "printf %s '" + encoded + "' | base64 -d | sh\n", nil
}

type consoleProbeOutputParser struct {
	markers [4]string
	line    []byte
	state   int
	result  ConsoleProbeResult
}

func (parser *consoleProbeOutputParser) consume(chunk []byte) (ConsoleProbeResult, bool, error) {
	for _, value := range chunk {
		if value != '\n' {
			parser.line = append(parser.line, value)
			continue
		}
		line := parser.line
		parser.line = nil
		line = bytes.TrimRight(line, "\r")
		if !utf8.Valid(line) {
			return ConsoleProbeResult{}, false, errors.New("terminal output line is not UTF-8")
		}
		complete, err := parser.consumeLine(string(line))
		if err != nil {
			return ConsoleProbeResult{}, false, err
		}
		if complete {
			return parser.result, true, nil
		}
	}
	return ConsoleProbeResult{}, false, nil
}

func (parser *consoleProbeOutputParser) consumeLine(line string) (bool, error) {
	switch parser.state {
	case 0:
		if line == parser.markers[0] {
			parser.state = 1
		}
	case 1:
		uid, err := strconv.Atoi(line)
		if err != nil || uid < 0 {
			return false, errors.New("terminal UID is invalid")
		}
		parser.result.UID = uid
		parser.state = 2
	case 2:
		if line != parser.markers[1] {
			return false, errors.New("terminal OS marker is missing")
		}
		parser.state = 3
	case 3:
		if !validConsoleValue(line) {
			return false, errors.New("terminal OS value is invalid")
		}
		parser.result.OS = line
		parser.state = 4
	case 4:
		if line != parser.markers[2] {
			return false, errors.New("terminal hostname marker is missing")
		}
		parser.state = 5
	case 5:
		if !validConsoleValue(line) {
			return false, errors.New("terminal hostname is invalid")
		}
		parser.result.Hostname = line
		parser.state = 6
	case 6:
		if line != parser.markers[3] {
			return false, errors.New("terminal end marker is missing")
		}
		parser.state = 7
		return true, nil
	}
	return false, nil
}

func parseConsoleProbeOutput(output []byte, markers [4]string) (ConsoleProbeResult, bool, error) {
	parser := consoleProbeOutputParser{markers: markers}
	return parser.consume(output)
}

func validConsoleValue(value string) bool {
	return value != "" && strings.TrimSpace(value) == value && utf8.ValidString(value)
}

func classifyConsoleSocketError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return fmt.Errorf("%w: %v", ErrConsoleCanceled, ctx.Err())
	}
	if errors.Is(err, websocket.ErrFrameTooLarge) {
		return ErrConsoleOutputLimit
	}
	return ErrConsoleDisconnected
}
