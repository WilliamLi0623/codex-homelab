package sessionruntime

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/WilliamLi0623/codex-homelab/internal/store"
	"golang.org/x/net/websocket"
)

const bootstrapGuestIdentityConsoleOutputLimit = 8 * 1024

// BootstrapGuestIdentityConsoleResult reports the guest identity observed
// during a completed fixed installer command. It is not bootstrap-stage
// completion evidence and does not establish SSH transport readiness.
type BootstrapGuestIdentityConsoleResult struct {
	UID             int
	OS              string
	Hostname        string
	IsolationSHA256 string
}

// InstallBootstrapGuestIdentity runs only the fixed GuestIdentity installer
// over the owned guest's shell console. Its caller must already hold a fresh
// durable GuestIdentity claim. This method does not access Store or complete
// the stage; the host public key remains the separate HostPin probe's input.
func (r *ProxmoxRuntime) InstallBootstrapGuestIdentity(ctx context.Context, binding store.SessionRuntimeBinding, clientPublicKey string) (BootstrapGuestIdentityConsoleResult, error) {
	var empty BootstrapGuestIdentityConsoleResult
	if ctx == nil {
		return empty, fmt.Errorf("%w: missing context", ErrConsoleCanceled)
	}
	if r == nil || !validMaterialBinding(binding) {
		return empty, ErrConsoleOwnership
	}
	markers, command, err := makeBootstrapGuestIdentityCommand(binding, clientPublicKey)
	if err != nil || !validBootstrapGuestIdentityMarkers(markers) {
		return empty, errBootstrapGuestIdentityCommand
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
	if err != nil || !validBootstrapEvidence(before) {
		if probeCtx.Err() != nil {
			return empty, fmt.Errorf("%w: %v", ErrConsoleCanceled, probeCtx.Err())
		}
		return empty, ErrBootstrapHostKeyIsolation
	}
	state, err := r.Observe(probeCtx, binding.VMID)
	if err != nil || state != RuntimeRunning {
		if probeCtx.Err() != nil {
			return empty, fmt.Errorf("%w: %v", ErrConsoleCanceled, probeCtx.Err())
		}
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
		return empty, ErrConsoleHandshake
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
	resizeFrame, err := encodeTermproxyResize(consoleProbeColumns, consoleProbeRows)
	if err != nil || websocket.Message.Send(conn, resizeFrame) != nil {
		return empty, classifyConsoleSocketError(probeCtx, errors.New("terminal resize failed"))
	}
	inputFrame, err := encodeTermproxyInput(command)
	if err != nil || websocket.Message.Send(conn, inputFrame) != nil {
		return empty, classifyConsoleSocketError(probeCtx, errors.New("terminal command send failed"))
	}

	parser := bootstrapGuestIdentityConsoleOutputParser{
		markers:         markers,
		expectedHost:    sessionHostname(binding.SessionID, binding.EpochID, binding.Generation),
		clientPublicKey: clientPublicKey,
	}
	outputBytes := 0
	for {
		var message []byte
		if err := websocket.Message.Receive(conn, &message); err != nil {
			if errors.Is(err, websocket.ErrFrameTooLarge) || outputBytes+len(message) > bootstrapGuestIdentityConsoleOutputLimit {
				return empty, ErrConsoleOutputLimit
			}
			if outputBytes > 0 && probeCtx.Err() == nil {
				return empty, ErrConsoleMalformedOutput
			}
			return empty, classifyConsoleSocketError(probeCtx, err)
		}
		if len(message) > consoleFrameLimit || len(message) > bootstrapGuestIdentityConsoleOutputLimit-outputBytes {
			return empty, ErrConsoleOutputLimit
		}
		outputBytes += len(message)
		observation, complete, parseErr := parser.consume(message)
		if parseErr != nil {
			if errors.Is(parseErr, ErrConsoleIdentityMismatch) {
				return empty, ErrConsoleIdentityMismatch
			}
			return empty, ErrConsoleMalformedOutput
		}
		if !complete {
			continue
		}
		if observation.UID != 0 || observation.OS != "Linux" || observation.Hostname != sessionHostname(binding.SessionID, binding.EpochID, binding.Generation) {
			return empty, ErrConsoleIdentityMismatch
		}
		if err := probeCtx.Err(); err != nil {
			return empty, fmt.Errorf("%w: %v", ErrConsoleCanceled, err)
		}
		after, err := r.VerifyBootstrapIsolation(probeCtx, binding)
		if err != nil || !validBootstrapEvidence(after) || before.SHA256 != after.SHA256 {
			if probeCtx.Err() != nil {
				return empty, fmt.Errorf("%w: %v", ErrConsoleCanceled, probeCtx.Err())
			}
			return empty, ErrBootstrapHostKeyIsolation
		}
		return BootstrapGuestIdentityConsoleResult{
			UID: observation.UID, OS: observation.OS, Hostname: observation.Hostname,
			IsolationSHA256: after.SHA256,
		}, nil
	}
}

type bootstrapGuestIdentityConsoleObservation struct {
	UID       int
	OS        string
	Hostname  string
	PublicKey string
}

type bootstrapGuestIdentityConsoleOutputParser struct {
	markers         [6]string
	line            []byte
	state           int
	expectedHost    string
	clientPublicKey string
	result          bootstrapGuestIdentityConsoleObservation
}

func validBootstrapGuestIdentityMarkers(markers [6]string) bool {
	if !validGuestIdentityMarker(markers[0]) {
		return false
	}
	base := strings.TrimSuffix(markers[0], "_BEGIN__")
	suffixes := [...]string{"_BEGIN__", "_UID__", "_OS__", "_HOST__", "_KEY__", "_END__"}
	seen := make(map[string]struct{}, len(markers))
	for index, marker := range markers {
		if marker != base+suffixes[index] || !validGuestIdentityMarker(marker) {
			return false
		}
		if _, exists := seen[marker]; exists {
			return false
		}
		seen[marker] = struct{}{}
	}
	return true
}

func (parser *bootstrapGuestIdentityConsoleOutputParser) consume(chunk []byte) (bootstrapGuestIdentityConsoleObservation, bool, error) {
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
			return bootstrapGuestIdentityConsoleObservation{}, false, errors.New("terminal output is not UTF-8")
		}
		complete, err := parser.consumeLine(string(line))
		if err != nil {
			return bootstrapGuestIdentityConsoleObservation{}, false, err
		}
		if complete {
			return parser.result, true, nil
		}
	}
	return bootstrapGuestIdentityConsoleObservation{}, false, nil
}

func (parser *bootstrapGuestIdentityConsoleOutputParser) consumeLine(line string) (bool, error) {
	markerIndex := -1
	for index, marker := range parser.markers {
		if line == marker {
			markerIndex = index
			break
		}
	}
	expectedMarker := map[int]int{0: 0, 2: 1, 4: 2, 6: 3, 8: 4, 9: 5}
	if parser.state == 0 {
		if markerIndex == 0 {
			parser.state = 1
		} else if markerIndex >= 0 {
			return false, errors.New("unexpected identity marker")
		}
		return false, nil
	}
	if index, markerState := expectedMarker[parser.state]; markerState {
		if markerIndex != index {
			return false, errors.New("identity marker missing or out of order")
		}
		switch parser.state {
		case 2:
			parser.state = 3
		case 4:
			parser.state = 5
		case 6:
			parser.state = 7
		case 8:
			parser.state = 9
		case 9:
			parser.state = 10
			return true, nil
		}
		return false, nil
	}
	if markerIndex >= 0 {
		return false, errors.New("duplicate identity marker")
	}
	switch parser.state {
	case 1:
		if line != "0" {
			return false, ErrConsoleIdentityMismatch
		}
		parser.result.UID = 0
		parser.state = 2
	case 3:
		if line != "Linux" {
			return false, ErrConsoleIdentityMismatch
		}
		parser.result.OS = line
		parser.state = 4
	case 5:
		if line != parser.expectedHost || !validConsoleValue(line) {
			return false, ErrConsoleIdentityMismatch
		}
		parser.result.Hostname = line
		parser.state = 6
	case 7:
		publicKey, ok := normalizeBootstrapHostPublicKey(line)
		if !ok || publicKey == parser.clientPublicKey {
			return false, errors.New("guest host public key is invalid")
		}
		parser.result.PublicKey = publicKey
		parser.state = 8
	default:
		return false, errors.New("unexpected guest identity output")
	}
	return false, nil
}
