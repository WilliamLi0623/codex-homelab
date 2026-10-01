package sessionruntime

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestBootstrapArchiveStream(t *testing.T) {
	payload := []byte{0, 1, 2, 255}
	var output bytes.Buffer
	result, err := decodeBootstrapArchiveStream(strings.NewReader("prompt\r\nBEGIN_TEST\r\nAAEC/w==\r\nEND_TEST\r\n"), &output, "BEGIN_TEST", "END_TEST", 4)
	digest := sha256.Sum256(payload)
	if err != nil || !bytes.Equal(output.Bytes(), payload) || result.Bytes != 4 || result.SHA256 != hex.EncodeToString(digest[:]) {
		t.Fatalf("result=%+v error=%v", result, err)
	}
}

func TestBootstrapArchiveStreamRejects(t *testing.T) {
	for name, input := range map[string]string{
		"missing begin": "AAEC/w==\nEND_TEST\n",
		"missing end":   "BEGIN_TEST\nAAEC/w==\n",
		"empty":         "BEGIN_TEST\nEND_TEST\n",
		"invalid":       "BEGIN_TEST\nSECRET!\nEND_TEST\n",
		"noncanonical":  "BEGIN_TEST\nAB==\nEND_TEST\n",
		"after padding": "BEGIN_TEST\nAA==\nAA==\nEND_TEST\n",
		"control":       "BEGIN_TEST\nAA\x1b==\nEND_TEST\n",
		"long line":     "BEGIN_TEST\n" + strings.Repeat("A", 4097) + "\nEND_TEST\n",
	} {
		t.Run(name, func(t *testing.T) {
			var out bytes.Buffer
			result, err := decodeBootstrapArchiveStream(strings.NewReader(input), &out, "BEGIN_TEST", "END_TEST", 4)
			if err == nil || result.Bytes != 0 || result.SHA256 != "" || strings.Contains(err.Error(), "SECRET") {
				t.Fatal("accepted invalid stream or disclosed input")
			}
		})
	}
	var out bytes.Buffer
	if _, err := decodeBootstrapArchiveStream(strings.NewReader("BEGIN_TEST\nAAEC/w==\nEND_TEST\n"), &out, "BEGIN_TEST", "END_TEST", 3); err == nil || out.Len() != 0 {
		t.Fatal("limit must be checked before writing")
	}
}

type bootstrapArchiveFailWriter struct{}

func (bootstrapArchiveFailWriter) Write(p []byte) (int, error) {
	return 0, errors.New("secret writer error")
}
func TestBootstrapArchiveStreamWriterFailure(t *testing.T) {
	for _, writer := range []io.Writer{bootstrapArchiveFailWriter{}, bootstrapArchiveShortWriter{}} {
		result, err := decodeBootstrapArchiveStream(strings.NewReader("BEGIN_TEST\nAAEC/w==\nEND_TEST\n"), writer, "BEGIN_TEST", "END_TEST", 4)
		if err == nil || result.SHA256 != "" || strings.Contains(err.Error(), "secret") {
			t.Fatal("writer failure not redacted")
		}
	}
}

type bootstrapArchiveShortWriter struct{}

func (bootstrapArchiveShortWriter) Write(p []byte) (int, error) { return len(p) - 1, nil }

type bootstrapArchiveFragmentReader struct{ io.Reader }

func (r bootstrapArchiveFragmentReader) Read(p []byte) (int, error) {
	if len(p) > 1 {
		p = p[:1]
	}
	return r.Reader.Read(p)
}
func TestBootstrapArchiveStreamFragmented(t *testing.T) {
	var out bytes.Buffer
	result, err := decodeBootstrapArchiveStream(bootstrapArchiveFragmentReader{strings.NewReader("BEGIN_TEST\nAAEC\n/w==\nEND_TEST\n")}, &out, "BEGIN_TEST", "END_TEST", 4)
	if err != nil || result.Bytes != 4 || !bytes.Equal(out.Bytes(), []byte{0, 1, 2, 255}) {
		t.Fatalf("fragmented stream: %+v %v", result, err)
	}
}
func TestBootstrapArchiveStreamBounds(t *testing.T) {
	for _, input := range []string{strings.Repeat("prompt\n", 1400) + "BEGIN_TEST\nAA==\nEND_TEST\n", "BEGIN_TEST\nAA==\nEND_TEST"} {
		var out bytes.Buffer
		if _, err := decodeBootstrapArchiveStream(strings.NewReader(input), &out, "BEGIN_TEST", "END_TEST", 4); err == nil {
			t.Fatal("accepted unbounded or unterminated framing")
		}
	}
	for _, marker := range []string{"short", "BEGIN\n_TEST", "BEGIN;TEST"} {
		var out bytes.Buffer
		if _, err := decodeBootstrapArchiveStream(strings.NewReader(""), &out, marker, "END_TEST", 4); err == nil {
			t.Fatal("accepted unsafe marker")
		}
	}
}

func TestBootstrapArchiveStreamRejectsBase64MarkerCollision(t *testing.T) {
	var out bytes.Buffer
	result, err := decodeBootstrapArchiveStream(strings.NewReader("BEGIN_TEST\nAAEC\nAAAAAAAA\n/w==\nAAAAAAAA\n"), &out, "BEGIN_TEST", "AAAAAAAA", 32)
	if err == nil || result.SHA256 != "" || out.Len() != 0 {
		t.Fatal("base64-only terminator accepted and truncated archive")
	}
}
