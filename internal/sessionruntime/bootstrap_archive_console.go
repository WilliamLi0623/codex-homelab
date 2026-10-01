package sessionruntime

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"

	"github.com/WilliamLi0623/codex-homelab/internal/store"
	"golang.org/x/net/websocket"
)

const (
	bootstrapTLSArchiveKeyGuestPath        = "/etc/ssl/private/ssl-cert-snakeoil.key"
	bootstrapTLSArchiveCertGuestPath       = "/etc/ssl/certs/ssl-cert-snakeoil.pem"
	bootstrapTLSArchiveDecodedLimit  int64 = 8 << 30
	bootstrapTLSArchiveWireLimit           = 12 << 30
	bootstrapTLSArchivePreambleLimit       = 8 << 10
)

type bootstrapTLSArchiveEvidence struct {
	Bytes           int64
	SHA256          string
	IsolationSHA256 string
}

// exportBootstrapTLSArchive is a fixed, read-only Proxmox console export.
// Partial destination output after any failure is unverified and must be quarantined by its caller.
func (r *ProxmoxRuntime) exportBootstrapTLSArchive(ctx context.Context, binding store.SessionRuntimeBinding, destination io.Writer) (bootstrapTLSArchiveEvidence, error) {
	return r.exportBootstrapArchivePolicy(ctx, binding, destination, bootstrapArchivePolicyTLS)
}

func (r *ProxmoxRuntime) exportBootstrapSessionImageArchive(ctx context.Context, binding store.SessionRuntimeBinding, destination io.Writer) (bootstrapTLSArchiveEvidence, error) {
	return r.exportBootstrapArchivePolicy(ctx, binding, destination, bootstrapArchivePolicySessionImageCredentialsV2)
}

func (r *ProxmoxRuntime) exportBootstrapArchivePolicy(ctx context.Context, binding store.SessionRuntimeBinding, destination io.Writer, policy bootstrapArchivePolicy) (bootstrapTLSArchiveEvidence, error) {
	var empty bootstrapTLSArchiveEvidence
	if _, ok := bootstrapArchivePolicyRoots(policy); !ok {
		return empty, ErrConsoleOwnership
	}
	if ctx == nil {
		return empty, fmt.Errorf("%w: missing context", ErrConsoleCanceled)
	}
	if r == nil || !validBootstrapHostKeyBinding(binding) || destination == nil {
		return empty, ErrConsoleOwnership
	}
	probeCtx, cancel := context.WithTimeout(ctx, consoleProbeDeadline)
	defer cancel()

	transport, err := consoleHTTPTransport(r.client)
	if err != nil {
		return empty, ErrConsoleHandshake
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

	markers, command, err := makeBootstrapArchiveCommand(policy)
	if err != nil {
		return empty, ErrConsoleMalformedOutput
	}
	resizeFrame, err := encodeTermproxyResize(consoleProbeColumns, consoleProbeRows)
	if err != nil {
		return empty, ErrConsoleMalformedOutput
	}
	if err := websocket.Message.Send(conn, resizeFrame); err != nil {
		return empty, classifyConsoleSocketError(probeCtx, err)
	}
	inputFrame, err := encodeTermproxyInput(command)
	if err != nil {
		return empty, ErrConsoleMalformedOutput
	}
	if err := websocket.Message.Send(conn, inputFrame); err != nil {
		return empty, classifyConsoleSocketError(probeCtx, err)
	}

	stream := &bootstrapArchiveConsoleStream{conn: conn, ctx: probeCtx}
	identity := &bootstrapArchiveConsoleIdentity{markers: markers}
	identity.expectedHost = sessionHostname(binding.SessionID, binding.EpochID, binding.Generation)
	verifiedStream := &bootstrapArchiveIdentityReader{input: stream, identity: identity}
	decoded, err := decodeBootstrapArchiveStream(verifiedStream, destination, markers[3], markers[4], bootstrapTLSArchiveDecodedLimit)
	if err != nil {
		if stream.failure != nil {
			return empty, stream.failure
		}
		if identity.failure != nil {
			return empty, identity.failure
		}
		return empty, ErrConsoleMalformedOutput
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
	if err := probeCtx.Err(); err != nil {
		return empty, fmt.Errorf("%w: %v", ErrConsoleCanceled, err)
	}
	return bootstrapTLSArchiveEvidence{Bytes: decoded.Bytes, SHA256: decoded.SHA256, IsolationSHA256: after.SHA256}, nil
}

func makeBootstrapTLSArchiveCommand() ([5]string, string, error) {
	return makeBootstrapArchiveCommand(bootstrapArchivePolicyTLS)
}

func makeBootstrapArchiveCommand(policy bootstrapArchivePolicy) ([5]string, string, error) {
	var markers [5]string
	roots, ok := bootstrapArchivePolicyRoots(policy)
	if !ok || len(roots) == 0 {
		return markers, "", ErrConsoleOwnership
	}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return markers, "", err
	}
	base := "__CODEX_ARCHIVE_" + hex.EncodeToString(nonce)
	markers = [5]string{base + "_UID__", base + "_OS__", base + "_HOST__", base + "_BEGIN__", base + "_END__"}
	paths := make([]string, 0, len(roots))
	for _, root := range roots {
		if root == "" || strings.HasPrefix(root, "/") || path.Clean(root) != root || strings.HasPrefix(root, "../") || strings.ContainsAny(root, "'\"`$\\ \t\n\r") {
			return [5]string{}, "", ErrConsoleOwnership
		}
		paths = append(paths, "/"+root)
	}
	var tarPaths strings.Builder
	for _, guestPath := range paths {
		tarPaths.WriteByte(' ')
		tarPaths.WriteString(guestPath)
	}
	script := "printf '%s\\n' '" + markers[0] + "'; id -u; " +
		"printf '%s\\n' '" + markers[1] + "'; uname -s; " +
		"printf '%s\\n' '" + markers[2] + "'; hostname; " +
		"printf '%s\\n' '" + markers[3] + "'; " +
		"bash -o pipefail -c 'tar -C / -cf - --" + tarPaths.String() + " 2>/dev/null | base64 -w 76 2>/dev/null' 2>/dev/null && " +
		"printf '%s\\n' '" + markers[4] + "'"
	encoded := base64.StdEncoding.EncodeToString([]byte(script))
	return markers, "printf %s '" + encoded + "' | base64 -d | sh\n", nil
}

type bootstrapArchiveConsoleStream struct {
	conn    *websocket.Conn
	ctx     context.Context
	pending []byte
	total   int64
	failure error
}

func (stream *bootstrapArchiveConsoleStream) Read(buffer []byte) (int, error) {
	if len(buffer) == 0 {
		return 0, nil
	}
	if len(stream.pending) == 0 {
		var message []byte
		if err := websocket.Message.Receive(stream.conn, &message); err != nil {
			if errors.Is(err, websocket.ErrFrameTooLarge) || int64(len(message)) > bootstrapTLSArchiveWireLimit-stream.total {
				stream.failure = ErrConsoleOutputLimit
			} else {
				stream.failure = classifyConsoleSocketError(stream.ctx, err)
			}
			return 0, stream.failure
		}
		if len(message) > consoleFrameLimit || int64(len(message)) > bootstrapTLSArchiveWireLimit-stream.total {
			stream.failure = ErrConsoleOutputLimit
			return 0, stream.failure
		}
		stream.total += int64(len(message))
		stream.pending = message
	}
	n := copy(buffer, stream.pending)
	stream.pending = stream.pending[n:]
	return n, nil
}

type bootstrapArchiveConsoleIdentity struct {
	markers      [5]string
	line         []byte
	bytes        int
	state        int
	uid          string
	os           string
	host         string
	expectedHost string
	ready        bool
	failure      error
}

func (reader *bootstrapArchiveIdentityReader) Read(buffer []byte) (int, error) {
	n, err := reader.input.Read(buffer)
	if n > 0 && !reader.identity.ready {
		if parseErr := reader.identity.consume(buffer[:n]); parseErr != nil {
			reader.identity.failure = parseErr
			return 0, parseErr
		}
	}
	return n, err
}

type bootstrapArchiveIdentityReader struct {
	input    io.Reader
	identity *bootstrapArchiveConsoleIdentity
}

func (identity *bootstrapArchiveConsoleIdentity) consume(chunk []byte) error {
	for _, value := range chunk {
		if identity.ready {
			return nil
		}
		identity.bytes++
		if identity.bytes > bootstrapTLSArchivePreambleLimit {
			return ErrConsoleOutputLimit
		}
		if value != '\n' {
			identity.line = append(identity.line, value)
			continue
		}
		line := strings.TrimSuffix(string(identity.line), "\r")
		identity.line = identity.line[:0]
		switch identity.state {
		case 0:
			if line == identity.markers[0] {
				identity.state = 1
			}
		case 1:
			identity.uid = line
			identity.state = 2
		case 2:
			if line != identity.markers[1] {
				return ErrConsoleMalformedOutput
			}
			identity.state = 3
		case 3:
			identity.os = line
			identity.state = 4
		case 4:
			if line != identity.markers[2] {
				return ErrConsoleMalformedOutput
			}
			identity.state = 5
		case 5:
			identity.host = line
			identity.state = 6
		case 6:
			if line != identity.markers[3] {
				return ErrConsoleMalformedOutput
			}
			if identity.uid != "0" || identity.os != "Linux" || identity.host != identity.expectedHost || !validConsoleValue(identity.host) {
				return ErrConsoleIdentityMismatch
			}
			identity.ready = true
		}
	}
	return nil
}
