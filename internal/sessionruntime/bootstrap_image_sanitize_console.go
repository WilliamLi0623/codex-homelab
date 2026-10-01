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

const bootstrapImageSanitationOutputLimit = 64 * 1024

type bootstrapImageSanitationResult struct {
	UID             int
	OS              string
	Hostname        string
	Policy          string
	IsolationSHA256 string
}

func (r *ProxmoxRuntime) sanitizeBootstrapImage(ctx context.Context, binding store.SessionRuntimeBinding) (bootstrapImageSanitationResult, error) {
	return r.runBootstrapImageSanitationConsole(ctx, binding, true)
}

func (r *ProxmoxRuntime) observeBootstrapImageSanitation(ctx context.Context, binding store.SessionRuntimeBinding) (bootstrapImageSanitationResult, error) {
	return r.runBootstrapImageSanitationConsole(ctx, binding, false)
}

func (r *ProxmoxRuntime) runBootstrapImageSanitationConsole(ctx context.Context, binding store.SessionRuntimeBinding, mutate bool) (bootstrapImageSanitationResult, error) {
	var empty bootstrapImageSanitationResult
	if ctx == nil {
		return empty, fmt.Errorf("%w: missing context", ErrConsoleCanceled)
	}
	if r == nil || !validMaterialBinding(binding) {
		return empty, ErrConsoleOwnership
	}
	policy := bootstrapArchivePolicySessionImageCredentialsV2
	var markers [6]string
	var command string
	var err error
	if mutate {
		markers, command, err = makeBootstrapImageSanitizeCommand(binding, policy)
	} else {
		markers, command, err = makeBootstrapImageSanitationObserverCommand(binding, policy)
	}
	if err != nil || !validBootstrapImageSanitationMarkers(markers) {
		return empty, errBootstrapImageSanitizeCommand
	}
	probeCtx, cancel := context.WithTimeout(ctx, consoleProbeDeadline)
	defer cancel()
	transport, err := consoleHTTPTransport(r.client)
	if err != nil {
		return empty, fmt.Errorf("%w: unsupported HTTP transport", ErrConsoleHandshake)
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

	parser := bootstrapImageSanitationOutputParser{markers: markers}
	outputBytes := 0
	var observation bootstrapImageSanitationOutput
	for {
		var message []byte
		if err := websocket.Message.Receive(conn, &message); err != nil {
			if errors.Is(err, websocket.ErrFrameTooLarge) || outputBytes+len(message) > bootstrapImageSanitationOutputLimit {
				return empty, ErrConsoleOutputLimit
			}
			if outputBytes > 0 && probeCtx.Err() == nil {
				return empty, ErrConsoleMalformedOutput
			}
			return empty, classifyConsoleSocketError(probeCtx, err)
		}
		if len(message) > consoleFrameLimit || len(message) > bootstrapImageSanitationOutputLimit-outputBytes {
			return empty, ErrConsoleOutputLimit
		}
		outputBytes += len(message)
		var complete bool
		observation, complete, err = parser.consume(message)
		if err != nil {
			if errors.Is(err, ErrConsoleIdentityMismatch) {
				return empty, err
			}
			return empty, ErrConsoleMalformedOutput
		}
		if !complete {
			continue
		}
		break
	}
	if observation.UID != 0 || observation.OS != "Linux" || observation.Hostname != sessionHostname(binding.SessionID, binding.EpochID, binding.Generation) || observation.AbsentCount != 23 {
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
	state, err = r.Observe(probeCtx, binding.VMID)
	if err != nil || state != RuntimeRunning {
		if probeCtx.Err() != nil {
			return empty, fmt.Errorf("%w: %v", ErrConsoleCanceled, probeCtx.Err())
		}
		return empty, ErrConsoleNotRunning
	}
	return bootstrapImageSanitationResult{UID: observation.UID, OS: observation.OS, Hostname: observation.Hostname, Policy: "session-image-credentials-v2", IsolationSHA256: after.SHA256}, nil
}

type bootstrapImageSanitationOutput struct {
	UID         int
	OS          string
	Hostname    string
	AbsentCount int
}

type bootstrapImageSanitationOutputParser struct {
	markers [6]string
	state   int
	line    []byte
	result  bootstrapImageSanitationOutput
}

func validBootstrapImageSanitationMarkers(markers [6]string) bool {
	base := strings.TrimSuffix(markers[0], "_UID__")
	if base == markers[0] || !strings.HasPrefix(base, "__CODEX_IMAGESAN_") {
		return false
	}
	suffixes := [...]string{"_UID__", "_OS__", "_HOST__", "_PREFLIGHT__", "_RESULT__", "_END__"}
	seen := make(map[string]struct{}, len(markers))
	for i, marker := range markers {
		if marker != base+suffixes[i] || !validArchiveMarker(marker) {
			return false
		}
		if _, ok := seen[marker]; ok {
			return false
		}
		seen[marker] = struct{}{}
	}
	return true
}

func (parser *bootstrapImageSanitationOutputParser) consume(chunk []byte) (bootstrapImageSanitationOutput, bool, error) {
	for _, b := range chunk {
		if b != '\n' {
			if len(parser.line) >= 16*1024 {
				return bootstrapImageSanitationOutput{}, false, ErrConsoleOutputLimit
			}
			parser.line = append(parser.line, b)
			continue
		}
		line := parser.line
		parser.line = nil
		if bytes.HasSuffix(line, []byte{'\r'}) {
			line = line[:len(line)-1]
		}
		if !utf8.Valid(line) {
			return bootstrapImageSanitationOutput{}, false, errors.New("terminal output is not UTF-8")
		}
		complete, err := parser.consumeLine(string(line))
		if err != nil {
			return bootstrapImageSanitationOutput{}, false, err
		}
		if complete {
			return parser.result, true, nil
		}
	}
	return bootstrapImageSanitationOutput{}, false, nil
}

func (parser *bootstrapImageSanitationOutputParser) consumeLine(line string) (bool, error) {
	switch parser.state {
	case 0:
		if line == parser.markers[0] {
			parser.state = 1
		}
	case 1:
		uid, err := strconv.Atoi(line)
		if err != nil || uid < 0 {
			return false, errors.New("terminal uid invalid")
		}
		parser.result.UID = uid
		parser.state = 2
	case 2:
		if line != parser.markers[1] {
			return false, errors.New("terminal uid marker missing")
		}
		parser.state = 3
	case 3:
		if !validConsoleValue(line) {
			return false, errors.New("terminal os invalid")
		}
		parser.result.OS = line
		parser.state = 4
	case 4:
		if line != parser.markers[2] {
			return false, errors.New("terminal os marker missing")
		}
		parser.state = 5
	case 5:
		if !validConsoleValue(line) {
			return false, errors.New("terminal hostname invalid")
		}
		parser.result.Hostname = line
		parser.state = 6
	case 6:
		if line != parser.markers[3] {
			return false, errors.New("terminal preflight marker missing")
		}
		parser.state = 7
	case 7:
		if line != parser.markers[4] {
			return false, errors.New("terminal result marker missing")
		}
		parser.state = 8
	case 8:
		if !strings.HasPrefix(line, "absent=") {
			return false, errors.New("terminal absent count missing")
		}
		count, err := strconv.Atoi(strings.TrimPrefix(line, "absent="))
		if err != nil || count < 0 {
			return false, errors.New("terminal absent count invalid")
		}
		parser.result.AbsentCount = count
		parser.state = 9
	case 9:
		if line != parser.markers[5] {
			return false, errors.New("terminal end marker missing")
		}
		parser.state = 10
		return true, nil
	}
	return false, nil
}
