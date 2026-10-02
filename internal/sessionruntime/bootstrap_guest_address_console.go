package sessionruntime

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/WilliamLi0623/codex-homelab/internal/store"
	"golang.org/x/net/websocket"
)

var ErrBootstrapGuestAddress = errors.New("bootstrap guest address probe failed")

type BootstrapGuestIPv4ProbeResult struct {
	Address      netip.Addr
	ConfigSHA256 string
}

// ProbeBootstrapGuestIPv4 reads the current private IPv4 from the owned,
// running Session guest. The address is intentionally returned only in memory;
// callers must not persist it as durable Session identity.
func (r *ProxmoxRuntime) ProbeBootstrapGuestIPv4(ctx context.Context, binding store.SessionRuntimeBinding) (BootstrapGuestIPv4ProbeResult, error) {
	var empty BootstrapGuestIPv4ProbeResult
	if r == nil || ctx == nil || !validMaterialBinding(binding) {
		return empty, ErrBootstrapGuestAddress
	}
	probeCtx, cancel := context.WithTimeout(ctx, consoleProbeDeadline)
	defer cancel()
	transport, err := consoleHTTPTransport(r.client)
	if err != nil {
		return empty, ErrBootstrapGuestAddress
	}
	if err := probeCtx.Err(); err != nil {
		return empty, fmt.Errorf("%w: %v", ErrBootstrapGuestAddress, err)
	}
	before, err := r.bootstrapNetworkConfig(probeCtx, binding)
	if err != nil || !bootstrapGuestAddressConfig(before, binding) {
		return empty, ErrBootstrapGuestAddress
	}
	networks, err := bootstrapEnabledNetworkProjection(before)
	if err != nil {
		return empty, ErrBootstrapGuestAddress
	}
	var beforeDigest string
	if json.Unmarshal(before["digest"], &beforeDigest) != nil || !pveConfigurationDigest.MatchString(beforeDigest) {
		return empty, ErrBootstrapGuestAddress
	}
	state, err := r.Observe(probeCtx, binding.VMID)
	if err != nil || state != RuntimeRunning {
		return empty, ErrBootstrapGuestAddress
	}
	if err := probeCtx.Err(); err != nil {
		return empty, fmt.Errorf("%w: %v", ErrBootstrapGuestAddress, err)
	}

	path := "/nodes/" + url.PathEscape(r.node) + "/lxc/" + strconv.Itoa(binding.VMID) + "/termproxy"
	var envelope struct {
		Data termproxySession `json:"data"`
	}
	if err := r.request(probeCtx, http.MethodPost, path, nil, &envelope); err != nil {
		return empty, ErrBootstrapGuestAddress
	}
	session := envelope.Data
	if session.User == "" || session.Ticket == "" || session.UPID == "" || session.Port < 5900 || session.Port > 5999 {
		return empty, ErrBootstrapGuestAddress
	}
	conn, err := dialTermproxy(probeCtx, r, binding.VMID, session, transport)
	if err != nil {
		return empty, ErrBootstrapGuestAddress
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
	if websocket.Message.Send(conn, []byte(session.User+":"+session.Ticket+"\n")) != nil {
		return empty, ErrBootstrapGuestAddress
	}
	var authReply [2]byte
	if _, err := io.ReadFull(conn, authReply[:]); err != nil || string(authReply[:]) != "OK" {
		return empty, ErrBootstrapGuestAddress
	}
	markers, command, err := makeBootstrapGuestAddressCommand()
	if err != nil {
		return empty, ErrBootstrapGuestAddress
	}
	resizeFrame, err := encodeTermproxyResize(consoleProbeColumns, consoleProbeRows)
	if err != nil || websocket.Message.Send(conn, resizeFrame) != nil {
		return empty, ErrBootstrapGuestAddress
	}
	inputFrame, err := encodeTermproxyInput(command)
	if err != nil || websocket.Message.Send(conn, inputFrame) != nil {
		return empty, ErrBootstrapGuestAddress
	}

	parser := bootstrapGuestAddressOutputParser{markers: markers}
	outputBytes := 0
	var address netip.Addr
	for {
		var message []byte
		if err := websocket.Message.Receive(conn, &message); err != nil {
			if errors.Is(err, websocket.ErrFrameTooLarge) || outputBytes+len(message) > consoleOutputLimit {
				return empty, ErrBootstrapGuestAddress
			}
			return empty, ErrBootstrapGuestAddress
		}
		if len(message) > consoleFrameLimit || outputBytes+len(message) > consoleOutputLimit {
			return empty, ErrBootstrapGuestAddress
		}
		outputBytes += len(message)
		var complete bool
		address, complete, err = parser.consume(message)
		if err != nil {
			return empty, ErrBootstrapGuestAddress
		}
		if complete {
			break
		}
	}
	if err := probeCtx.Err(); err != nil {
		return empty, fmt.Errorf("%w: %v", ErrBootstrapGuestAddress, err)
	}
	after, err := r.bootstrapNetworkConfig(probeCtx, binding)
	if err != nil || !bootstrapGuestAddressConfig(after, binding) {
		return empty, ErrBootstrapGuestAddress
	}
	var afterDigest string
	if json.Unmarshal(after["digest"], &afterDigest) != nil || beforeDigest != afterDigest {
		return empty, ErrBootstrapGuestAddress
	}
	afterNetworks, err := bootstrapEnabledNetworkProjection(after)
	if err != nil {
		return empty, ErrBootstrapGuestAddress
	}
	evidence, err := bootstrapNetworkEvidence(binding, afterNetworks)
	if err != nil || networks["net0"] != afterNetworks["net0"] {
		return empty, ErrBootstrapGuestAddress
	}
	return BootstrapGuestIPv4ProbeResult{Address: address, ConfigSHA256: evidence.SHA256}, nil
}

func bootstrapGuestAddressConfig(config map[string]json.RawMessage, binding store.SessionRuntimeBinding) bool {
	if !bootstrapNetworkSnapshotMatchesBinding(config, binding) {
		return false
	}
	_, err := bootstrapEnabledNetworkProjection(config)
	return err == nil
}

func makeBootstrapGuestAddressCommand() ([3]string, string, error) {
	var markers [3]string
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return markers, "", err
	}
	base := "__CODEX_GUESTADDR_" + fmt.Sprintf("%x", nonce)
	markers = [3]string{base + "_BEGIN__", base + "_IP__", base + "_END__"}
	script := "printf '%s\\n' '" + markers[0] + "'; ip -o -4 addr show dev eth0 scope global | awk '{print $4}'; printf '%s\\n' '" + markers[1] + "'; printf '%s\\n' '" + markers[2] + "'"
	encoded := base64.StdEncoding.EncodeToString([]byte(script))
	return markers, "printf %s '" + encoded + "' | base64 -d | sh\n", nil
}

type bootstrapGuestAddressOutputParser struct {
	markers [3]string
	line    []byte
	state   int
	address netip.Addr
}

func (parser *bootstrapGuestAddressOutputParser) consume(chunk []byte) (netip.Addr, bool, error) {
	for _, value := range chunk {
		if value != '\n' {
			parser.line = append(parser.line, value)
			if len(parser.line) > 128 {
				return netip.Addr{}, false, ErrBootstrapGuestAddress
			}
			continue
		}
		line := parser.line
		parser.line = nil
		if bytes.HasSuffix(line, []byte{'\r'}) {
			line = line[:len(line)-1]
		}
		if !utf8.Valid(line) {
			return netip.Addr{}, false, ErrBootstrapGuestAddress
		}
		switch parser.state {
		case 0:
			if string(line) == parser.markers[0] {
				parser.state = 1
			}
		case 1:
			address, err := parseBootstrapGuestPrivateIPv4(string(line))
			if err != nil {
				return netip.Addr{}, false, err
			}
			parser.address = address
			parser.state = 2
		case 2:
			if string(line) != parser.markers[1] {
				return netip.Addr{}, false, ErrBootstrapGuestAddress
			}
			parser.state = 3
		case 3:
			if string(line) != parser.markers[2] {
				return netip.Addr{}, false, ErrBootstrapGuestAddress
			}
			parser.state = 4
			return parser.address, true, nil
		}
	}
	return netip.Addr{}, false, nil
}

func parseBootstrapGuestPrivateIPv4(value string) (netip.Addr, error) {
	if strings.ContainsAny(value, " \t\r\n") {
		return netip.Addr{}, ErrBootstrapGuestAddress
	}
	prefix, err := netip.ParsePrefix(value)
	if err != nil || !prefix.Addr().Is4() || prefix.Bits() < 1 {
		return netip.Addr{}, ErrBootstrapGuestAddress
	}
	address := prefix.Addr()
	if !address.IsPrivate() || address.IsLoopback() || address.IsLinkLocalUnicast() || address.IsMulticast() || address.IsUnspecified() || prefix.Masked().Addr() == address {
		return netip.Addr{}, ErrBootstrapGuestAddress
	}
	bits := prefix.Bits()
	mask := uint32(0)
	if bits < 32 {
		mask = ^uint32(0) << (32 - bits)
	}
	raw := address.As4()
	value32 := uint32(raw[0])<<24 | uint32(raw[1])<<16 | uint32(raw[2])<<8 | uint32(raw[3])
	if bits < 31 && value32 == ((value32&mask)|^mask) {
		return netip.Addr{}, ErrBootstrapGuestAddress
	}
	return address, nil
}
