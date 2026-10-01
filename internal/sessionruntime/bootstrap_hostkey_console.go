package sessionruntime

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/WilliamLi0623/codex-homelab/internal/store"
	"golang.org/x/net/websocket"
)

const (
	bootstrapHostKeyGuestPath   = "/etc/ssh/ssh_host_p28_ed25519_key.pub"
	bootstrapHostKeyOutputLimit = 8 * 1024
)

var ErrBootstrapHostKeyIsolation = errors.New("bootstrap host-key probe requires an owned fenced guest")

// BootstrapHostKeyProbeResult contains the observed public key and the
// non-secret guest identity and isolation proof. It makes no readiness claim.
type BootstrapHostKeyProbeResult struct {
	PublicKey       string
	UID             int
	OS              string
	Hostname        string
	IsolationSHA256 string
}

// ProbeBootstrapHostKey performs a fixed read-only console observation while
// the owned guest is network-fenced. The binding is the only caller input; the
// command and guest path are fixed here.
func (r *ProxmoxRuntime) ProbeBootstrapHostKey(ctx context.Context, binding store.SessionRuntimeBinding) (BootstrapHostKeyProbeResult, error) {
	var empty BootstrapHostKeyProbeResult
	if ctx == nil {
		return empty, fmt.Errorf("%w: missing context", ErrConsoleCanceled)
	}
	if r == nil || !validBootstrapHostKeyBinding(binding) {
		return empty, ErrConsoleOwnership
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
	before, err := r.VerifyBootstrapIsolation(probeCtx, binding)
	if err != nil {
		if probeCtx.Err() != nil {
			return empty, fmt.Errorf("%w: %v", ErrConsoleCanceled, probeCtx.Err())
		}
		return empty, ErrBootstrapHostKeyIsolation
	}
	if !validBootstrapEvidence(before) {
		return empty, ErrBootstrapHostKeyIsolation
	}
	state, err := r.Observe(probeCtx, binding.VMID)
	if err != nil {
		if probeCtx.Err() != nil {
			return empty, fmt.Errorf("%w: %v", ErrConsoleCanceled, probeCtx.Err())
		}
		return empty, ErrConsoleNotRunning
	}
	if state != RuntimeRunning {
		return empty, ErrConsoleNotRunning
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
		return empty, ErrConsoleHandshake
	}

	markers, command, err := makeBootstrapHostKeyCommand()
	if err != nil {
		return empty, ErrConsoleMalformedOutput
	}
	resizeFrame, err := encodeTermproxyResize(consoleProbeColumns, consoleProbeRows)
	if err != nil || websocket.Message.Send(conn, resizeFrame) != nil {
		return empty, classifyConsoleSocketError(probeCtx, errors.New("terminal write failed"))
	}
	inputFrame, err := encodeTermproxyInput(command)
	if err != nil || websocket.Message.Send(conn, inputFrame) != nil {
		return empty, classifyConsoleSocketError(probeCtx, errors.New("terminal write failed"))
	}

	outputBytes := 0
	parser := bootstrapHostKeyOutputParser{markers: markers}
	var observed bootstrapHostKeyObservation
	for {
		var message []byte
		if err := websocket.Message.Receive(conn, &message); err != nil {
			if errors.Is(err, websocket.ErrFrameTooLarge) {
				return empty, ErrConsoleOutputLimit
			}
			if outputBytes > 0 && probeCtx.Err() == nil {
				return empty, ErrConsoleMalformedOutput
			}
			return empty, classifyConsoleSocketError(probeCtx, err)
		}
		if len(message) > consoleFrameLimit || outputBytes+len(message) > bootstrapHostKeyOutputLimit {
			return empty, ErrConsoleOutputLimit
		}
		outputBytes += len(message)
		var complete bool
		observed, complete, err = parser.consume(message)
		if err != nil {
			return empty, ErrConsoleMalformedOutput
		}
		if !complete {
			continue
		}
		if observed.UID != 0 || observed.OS != "Linux" || observed.Hostname != sessionHostname(binding.SessionID, binding.EpochID, binding.Generation) {
			return empty, ErrConsoleIdentityMismatch
		}
		break
	}

	if err := probeCtx.Err(); err != nil {
		return empty, fmt.Errorf("%w: %v", ErrConsoleCanceled, err)
	}
	after, err := r.VerifyBootstrapIsolation(probeCtx, binding)
	if err != nil {
		if probeCtx.Err() != nil {
			return empty, fmt.Errorf("%w: %v", ErrConsoleCanceled, probeCtx.Err())
		}
		return empty, ErrBootstrapHostKeyIsolation
	}
	if before.SHA256 != after.SHA256 {
		return empty, ErrBootstrapHostKeyIsolation
	}
	return BootstrapHostKeyProbeResult{
		PublicKey:       observed.PublicKey,
		UID:             observed.UID,
		OS:              observed.OS,
		Hostname:        observed.Hostname,
		IsolationSHA256: after.SHA256,
	}, nil
}

type bootstrapHostKeyObservation struct {
	UID       int
	OS        string
	Hostname  string
	PublicKey string
}

type bootstrapHostKeyOutputParser struct {
	markers [6]string
	line    []byte
	state   int
	result  bootstrapHostKeyObservation
}

func (parser *bootstrapHostKeyOutputParser) consume(chunk []byte) (bootstrapHostKeyObservation, bool, error) {
	for _, value := range chunk {
		if value != '\n' {
			parser.line = append(parser.line, value)
			continue
		}
		line := parser.line
		parser.line = nil
		if bytes.HasSuffix(line, []byte{'\r'}) {
			line = line[:len(line)-1]
		}
		if !utf8.Valid(line) {
			return bootstrapHostKeyObservation{}, false, errors.New("terminal output is not UTF-8")
		}
		complete, err := parser.consumeLine(string(line))
		if err != nil {
			return bootstrapHostKeyObservation{}, false, err
		}
		if complete {
			return parser.result, true, nil
		}
	}
	return bootstrapHostKeyObservation{}, false, nil
}

func (parser *bootstrapHostKeyOutputParser) consumeLine(line string) (bool, error) {
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
			return false, errors.New("terminal UID marker is missing")
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
			return false, errors.New("terminal OS marker is missing")
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
			return false, errors.New("terminal hostname marker is missing")
		}
		parser.state = 7
	case 7:
		publicKey, ok := normalizeBootstrapHostPublicKey(line)
		if !ok {
			return false, errors.New("terminal host public key is invalid")
		}
		parser.result.PublicKey = publicKey
		parser.state = 8
	case 8:
		if line != parser.markers[4] {
			return false, errors.New("terminal host key marker is missing")
		}
		parser.state = 9
	case 9:
		if line != parser.markers[5] {
			return false, errors.New("terminal end marker is missing")
		}
		parser.state = 10
		return true, nil
	}
	return false, nil
}

// OpenSSH .pub lines may append a user-facing comment after the key blob.
// Drop that comment, then apply the existing strict type and blob validator to
// the canonical two-field value returned to the caller.
func normalizeBootstrapHostPublicKey(line string) (string, bool) {
	if !utf8.ValidString(line) || strings.ContainsAny(line, "\x00\r\n") {
		return "", false
	}
	for _, value := range line {
		if unicode.IsControl(value) {
			return "", false
		}
	}
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return "", false
	}
	key := fields[0] + " " + fields[1]
	if !validMaterialPublicKey(key) {
		return "", false
	}
	return key, true
}

func makeBootstrapHostKeyCommand() ([6]string, string, error) {
	var markers [6]string
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return markers, "", err
	}
	base := "__CODEX_HOSTKEY_" + base64.RawURLEncoding.EncodeToString(nonce)
	markers = [6]string{base + "_BEGIN__", base + "_UID__", base + "_OS__", base + "_HOST__", base + "_KEY__", base + "_END__"}
	script := "printf '%s\\n' '" + markers[0] + "'; id -u; printf '%s\\n' '" + markers[1] + "'; uname -s; printf '%s\\n' '" + markers[2] + "'; hostname; printf '%s\\n' '" + markers[3] + "'; cat -- " + bootstrapHostKeyGuestPath + "; printf '%s\\n' '" + markers[4] + "'; printf '%s\\n' '" + markers[5] + "'"
	encoded := base64.StdEncoding.EncodeToString([]byte(script))
	return markers, "printf %s '" + encoded + "' | base64 -d | sh\n", nil
}

func validBootstrapHostKeyBinding(binding store.SessionRuntimeBinding) bool {
	for _, value := range []string{binding.SessionID, binding.EpochID, binding.Generation} {
		if value == "" || len(value) > 256 || strings.TrimSpace(value) != value || strings.ContainsAny(value, "\x00\r\n") {
			return false
		}
	}
	return validSessionVMID(binding.VMID)
}
