package sessionruntime

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
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

const bootstrapGuestIdentityObserveOutputLimit = 8 * 1024

// makeBootstrapGuestIdentityObserveCommand builds a fixed, read-only guest
// observation. Binding and client-key bytes enter the script only as base64.
func makeBootstrapGuestIdentityObserveCommand(binding store.SessionRuntimeBinding, clientPublicKey string) ([10]string, string, []byte, error) {
	var markers [10]string
	if !validMaterialBinding(binding) || !validMaterialPublicKey(clientPublicKey) {
		return markers, "", nil, errBootstrapGuestIdentityCommand
	}
	expectedMarker, err := bootstrapGuestIdentityMarkerData(binding, clientPublicKey)
	if err != nil {
		return markers, "", nil, errBootstrapGuestIdentityCommand
	}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return markers, "", nil, errBootstrapGuestIdentityCommand
	}
	base := "__CODEX_GUESTOBS_" + hex.EncodeToString(nonce)
	captureToken := base + "_CAPTURE__"
	suffixes := [...]string{"_BEGIN__", "_UID__", "_OS__", "_HOST__", "_MARKER__", "_DERIVED__", "_STORED__", "_AUTHORIZED__", "_SSHD__", "_END__"}
	for index, suffix := range suffixes {
		markers[index] = base + suffix
	}
	clientKeyB64 := base64.StdEncoding.EncodeToString([]byte(clientPublicKey))
	markerPath := bootstrapGuestIdentityMarkerPath
	hostKeyPath := bootstrapGuestIdentityHostKeyPath
	hostPubPath := bootstrapGuestIdentityHostPublicKeyPath
	authorizedPath := bootstrapGuestIdentityAuthorizedKeysPath
	script := "set -eu\n" +
		"exec 2>/dev/null\n" +
		"LC_ALL=C; export LC_ALL\n" +
		"set -o pipefail\n" +
		"ulimit -v 131072 || exit 1\n" +
		"CAPTURE_TOKEN='" + captureToken + "'\n" +
		"capture_bounded() { token=$1; limit=$2; shift 2; captured=$({ \"$@\" || exit $?; printf '\\n%s\\n' \"$token\"; } | head -c \"$((limit + ${#token} + 2))\") || return 1; case \"$captured\" in *\"$token\") CAPTURED=${captured%\"$token\"}; CAPTURED=${CAPTURED%?} ;; *) return 1 ;; esac; [ \"${#CAPTURED}\" -le \"$limit\" ] || return 1; }\n" +
		"CLIENT_KEY_B64='" + clientKeyB64 + "'\n" +
		"CLIENT_KEY=$(printf '%s' \"$CLIENT_KEY_B64\" | base64 -d) || exit 1\n" +
		"printf '%s\\n' '" + markers[0] + "'\n" +
		"printf '%s\\n' '" + markers[1] + "'; id -u\n" +
		"printf '%s\\n' '" + markers[2] + "'; uname -s\n" +
		"printf '%s\\n' '" + markers[3] + "'; hostname\n" +
		"printf '%s\\n' '" + markers[4] + "'; marker_size=$(stat -c %s " + markerPath + ") || exit 1; [ \"$marker_size\" -le 4096 ] || exit 1; capture_bounded \"$CAPTURE_TOKEN\" 8192 base64 -w 0 " + markerPath + " || exit 1; marker=$CAPTURED; printf '%s\\n' \"$marker\"\n" +
		"printf '%s\\n' '" + markers[5] + "'; capture_bounded \"$CAPTURE_TOKEN\" 4096 ssh-keygen -y -f " + hostKeyPath + " || exit 1; derived=$CAPTURED; printf '%s\\n' \"$derived\"\n" +
		"printf '%s\\n' '" + markers[6] + "'; pub_size=$(stat -c %s " + hostPubPath + ") || exit 1; [ \"$pub_size\" -le 8192 ] || exit 1; capture_bounded \"$CAPTURE_TOKEN\" 8192 awk 'NF >= 2 {print $1 \" \" $2}' " + hostPubPath + " || exit 1; stored=$CAPTURED; printf '%s\\n' \"$stored\"\n" +
		"printf '%s\\n' '" + markers[7] + "'; authorized_size=$(stat -c %s " + authorizedPath + ") || exit 1; [ \"$authorized_size\" -le 1048576 ] || exit 1; count=$(awk -v expected=\"$CLIENT_KEY\" '$0 == expected {n++} END {print n+0}' " + authorizedPath + ") || exit 1; printf '%s\\n' \"$count\"\n" +
		"sshd -t || exit 1\n" +
		"capture_bounded \"$CAPTURE_TOKEN\" 8192 sshd -T || exit 1; effective=$CAPTURED\n" +
		"[ ${#effective} -le 16384 ] || exit 1\n" +
		"printf '%s\\n' '" + markers[8] + "'; printf '%s\\n' \"$effective\" | awk '$1 == \"hostkey\" || $1 == \"passwordauthentication\" || $1 == \"kbdinteractiveauthentication\" || $1 == \"pubkeyauthentication\"'\n" +
		"printf '%s\\n' '" + markers[9] + "'\n"
	encoded := base64.StdEncoding.EncodeToString([]byte(script))
	return markers, "printf %s '" + encoded + "' | base64 -d | bash\n", expectedMarker, nil
}

// ObserveBootstrapGuestIdentity reconciles the guest's installed identity
// without repairing it, writing to Store, or completing a bootstrap stage.
// Its caller supplies the binding and exact client key expected by the durable
// identity marker. The result contains no public or private key material.
func (r *ProxmoxRuntime) ObserveBootstrapGuestIdentity(ctx context.Context, binding store.SessionRuntimeBinding, expectedClientPublicKey string) (BootstrapGuestIdentityConsoleResult, error) {
	var empty BootstrapGuestIdentityConsoleResult
	if ctx == nil {
		return empty, fmt.Errorf("%w: missing context", ErrConsoleCanceled)
	}
	if r == nil || !validMaterialBinding(binding) {
		return empty, ErrConsoleOwnership
	}
	markers, command, expectedMarker, err := makeBootstrapGuestIdentityObserveCommand(binding, expectedClientPublicKey)
	if err != nil || !validBootstrapGuestIdentityObserveMarkers(markers) {
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

	parser := bootstrapGuestIdentityObserveOutputParser{
		markers: markers, expectedHost: sessionHostname(binding.SessionID, binding.EpochID, binding.Generation),
		clientPublicKey: expectedClientPublicKey, expectedMarker: expectedMarker, binding: binding,
	}
	outputBytes := 0
	for {
		var message []byte
		if err := websocket.Message.Receive(conn, &message); err != nil {
			if errors.Is(err, websocket.ErrFrameTooLarge) || outputBytes+len(message) > bootstrapGuestIdentityObserveOutputLimit {
				return empty, ErrConsoleOutputLimit
			}
			if outputBytes > 0 && probeCtx.Err() == nil {
				return empty, ErrConsoleMalformedOutput
			}
			return empty, classifyConsoleSocketError(probeCtx, err)
		}
		if len(message) > consoleFrameLimit || len(message) > bootstrapGuestIdentityObserveOutputLimit-outputBytes {
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

func validBootstrapGuestIdentityObserveMarkers(markers [10]string) bool {
	if !strings.HasPrefix(markers[0], "__CODEX_GUESTOBS_") || !strings.HasSuffix(markers[0], "_BEGIN__") {
		return false
	}
	base := strings.TrimSuffix(markers[0], "_BEGIN__")
	if len(base) != len("__CODEX_GUESTOBS_")+32 {
		return false
	}
	if _, err := hex.DecodeString(strings.TrimPrefix(base, "__CODEX_GUESTOBS_")); err != nil {
		return false
	}
	suffixes := [...]string{"_BEGIN__", "_UID__", "_OS__", "_HOST__", "_MARKER__", "_DERIVED__", "_STORED__", "_AUTHORIZED__", "_SSHD__", "_END__"}
	seen := make(map[string]struct{}, len(markers))
	for index, marker := range markers {
		if marker != base+suffixes[index] {
			return false
		}
		if _, exists := seen[marker]; exists {
			return false
		}
		seen[marker] = struct{}{}
	}
	return true
}

type bootstrapGuestIdentityObserveObservation struct {
	UID      int
	OS       string
	Hostname string
	HostKey  string
}

type bootstrapGuestIdentityObserveOutputParser struct {
	markers         [10]string
	line            []byte
	state           int
	expectedHost    string
	clientPublicKey string
	expectedMarker  []byte
	binding         store.SessionRuntimeBinding
	result          bootstrapGuestIdentityObserveObservation
	hostKeyCount    int
	passwordNo      bool
	kbdNo           bool
	pubkeyYes       bool
}

func (parser *bootstrapGuestIdentityObserveOutputParser) consume(chunk []byte) (bootstrapGuestIdentityObserveObservation, bool, error) {
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
			return bootstrapGuestIdentityObserveObservation{}, false, errors.New("terminal output is not UTF-8")
		}
		complete, err := parser.consumeLine(string(line))
		if err != nil {
			return bootstrapGuestIdentityObserveObservation{}, false, err
		}
		if complete {
			return parser.result, true, nil
		}
	}
	return bootstrapGuestIdentityObserveObservation{}, false, nil
}

func (parser *bootstrapGuestIdentityObserveOutputParser) consumeLine(line string) (bool, error) {
	markerIndex := -1
	for index, marker := range parser.markers {
		if line == marker {
			markerIndex = index
			break
		}
	}
	if parser.state == 0 {
		if markerIndex == 0 {
			parser.state = 1
		} else if markerIndex >= 0 {
			return false, errors.New("unexpected observation marker")
		}
		return false, nil
	}
	if parser.state == 16 {
		if markerIndex == 9 {
			if parser.hostKeyCount != 1 || !parser.passwordNo || !parser.kbdNo || !parser.pubkeyYes {
				return false, errors.New("effective SSH configuration is incomplete")
			}
			return true, nil
		}
		if markerIndex >= 0 {
			return false, errors.New("unexpected observation marker")
		}
		return false, parser.consumeSSHDLine(line)
	}
	wantMarker := -1
	switch parser.state {
	case 1:
		wantMarker = 1
	case 3:
		wantMarker = 2
	case 5:
		wantMarker = 3
	case 7:
		wantMarker = 4
	case 9:
		wantMarker = 5
	case 11:
		wantMarker = 6
	case 13:
		wantMarker = 7
	case 15:
		wantMarker = 8
	}
	if wantMarker >= 0 {
		want := wantMarker
		if markerIndex != want {
			return false, errors.New("observation marker missing or out of order")
		}
		parser.state++
		if parser.state == 16 {
			return false, nil
		}
		return false, nil
	}
	if markerIndex >= 0 {
		return false, errors.New("duplicate observation marker")
	}
	switch parser.state {
	case 2:
		if line != "0" {
			return false, ErrConsoleIdentityMismatch
		}
		parser.result.UID = 0
	case 4:
		if line != "Linux" {
			return false, ErrConsoleIdentityMismatch
		}
		parser.result.OS = line
	case 6:
		if line != parser.expectedHost || !validConsoleValue(line) {
			return false, ErrConsoleIdentityMismatch
		}
		parser.result.Hostname = line
	case 8:
		data, err := base64.StdEncoding.Strict().DecodeString(line)
		if err != nil || !bytes.Equal(data, parser.expectedMarker) || !bootstrapGuestIdentityMarkerMatches(data, parser.binding, parser.clientPublicKey) {
			return false, ErrConsoleIdentityMismatch
		}
	case 10:
		key, ok := normalizeBootstrapHostPublicKey(line)
		if !ok || key == parser.clientPublicKey {
			return false, errors.New("derived host public key is invalid")
		}
		parser.result.HostKey = key
	case 12:
		key, ok := normalizeBootstrapHostPublicKey(line)
		if !ok || key != parser.result.HostKey {
			return false, errors.New("stored host public key does not match private key")
		}
	case 14:
		if line != "1" {
			return false, errors.New("client key is not uniquely authorized")
		}
	default:
		return false, errors.New("unexpected observation output")
	}
	parser.state++
	return false, nil
}

func (parser *bootstrapGuestIdentityObserveOutputParser) consumeSSHDLine(line string) error {
	fields := strings.Fields(line)
	if len(fields) != 2 {
		return errors.New("malformed effective SSH directive")
	}
	switch fields[0] {
	case "hostkey":
		parser.hostKeyCount++
		if fields[1] != bootstrapGuestIdentityHostKeyPath {
			return errors.New("unexpected effective host key")
		}
	case "passwordauthentication":
		if parser.passwordNo || fields[1] != "no" {
			return errors.New("password authentication remains enabled")
		}
		parser.passwordNo = true
	case "kbdinteractiveauthentication":
		if parser.kbdNo || fields[1] != "no" {
			return errors.New("keyboard interactive authentication remains enabled")
		}
		parser.kbdNo = true
	case "pubkeyauthentication":
		if parser.pubkeyYes || fields[1] != "yes" {
			return errors.New("public key authentication is disabled")
		}
		parser.pubkeyYes = true
	default:
		return errors.New("unexpected effective SSH directive")
	}
	return nil
}
