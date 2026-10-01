package sessionruntime

import (
	"bufio"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"strings"
)

var errBootstrapArchiveStream = errors.New("bootstrap archive stream could not be verified")

type bootstrapArchiveStreamResult struct {
	Bytes  int64
	SHA256 string
}

// decodeBootstrapArchiveStream consumes a bounded, line-framed base64 export.
// A successful result proves framing and byte integrity only, not archive
// semantics, guest provenance, durable storage or permission to sanitize.
// On failure the caller must quarantine the partial destination, never retry
// an ambiguous guest mutation. No upstream or writer error text is exposed.
func decodeBootstrapArchiveStream(input io.Reader, output io.Writer, begin, end string, limit int64) (bootstrapArchiveStreamResult, error) {
	empty := bootstrapArchiveStreamResult{}
	if input == nil || output == nil || !validArchiveMarker(begin) || !validArchiveMarker(end) || begin == end || limit <= 0 || limit > 8<<30 {
		return empty, errBootstrapArchiveStream
	}
	reader := bufio.NewReaderSize(input, 4096)
	digest := sha256.New()
	var count int64
	preamble := 0
	started, padded := false, false
	for {
		line, err := reader.ReadSlice('\n')
		if err != nil || len(line) > 4096 {
			return empty, errBootstrapArchiveStream
		}
		preambleLength := len(line)
		line = line[:len(line)-1]
		if len(line) > 0 && line[len(line)-1] == '\r' {
			line = line[:len(line)-1]
		}
		if !started {
			if string(line) == begin {
				started = true
				continue
			}
			preamble += preambleLength
			if preamble > 8192 {
				return empty, errBootstrapArchiveStream
			}
			continue
		}
		if string(line) == end {
			if count == 0 {
				return empty, errBootstrapArchiveStream
			}
			return bootstrapArchiveStreamResult{Bytes: count, SHA256: hex.EncodeToString(digest.Sum(nil))}, nil
		}
		if padded || len(line) == 0 {
			return empty, errBootstrapArchiveStream
		}
		// Strict rejects nonzero trailing bits; round-trip also excludes CR/LF,
		// which Go's base64 decoder otherwise deliberately ignores.
		decoded, err := base64.StdEncoding.Strict().DecodeString(string(line))
		if err != nil || base64.StdEncoding.EncodeToString(decoded) != string(line) || int64(len(decoded)) > limit-count {
			return empty, errBootstrapArchiveStream
		}
		n, err := output.Write(decoded)
		if err != nil || n != len(decoded) {
			return empty, errBootstrapArchiveStream
		}
		_, _ = digest.Write(decoded)
		count += int64(n)
		padded = strings.Contains(string(line), "=")
	}
}

func validArchiveMarker(marker string) bool {
	// An underscore is outside standard base64: no payload line can ever
	// collide with either framing marker.
	if len(marker) < 8 || len(marker) > 128 || !strings.Contains(marker, "_") {
		return false
	}
	for _, c := range marker {
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_') {
			return false
		}
	}
	return true
}
