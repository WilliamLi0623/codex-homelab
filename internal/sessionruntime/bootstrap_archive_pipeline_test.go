package sessionruntime

import (
	"archive/tar"
	"bytes"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
)

func TestBootstrapArchivePipelineRejectsPendingGNUHeader(t *testing.T) {
	base := bootstrapSanitationArchive(t, nil)
	var metadata bytes.Buffer
	w := tar.NewWriter(&metadata)
	body := []byte("pending-name\x00")
	if err := w.WriteHeader(&tar.Header{Name: "pending", Typeflag: tar.TypeReg, Mode: 0600, Size: int64(len(body)), Format: tar.FormatUSTAR}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	data := append([]byte(nil), metadata.Bytes()[:metadata.Len()-1024]...)
	// Construct a valid hidden GNU metadata header independently of the
	// production validator; Go's writer forbids emitting it directly.
	data[156] = tar.TypeGNULongName
	for i := 148; i < 156; i++ {
		data[i] = ' '
	}
	var checksum int
	for _, b := range data[:512] {
		checksum += int(b)
	}
	copy(data[148:156], []byte(fmt.Sprintf("%06o\x00 ", checksum)))
	archive := append(append([]byte(nil), base[:len(base)-1024]...), data...)
	if err := validateBootstrapSanitationArchive(bytes.NewReader(archive), bootstrapArchivePolicyTLS); err == nil {
		t.Fatal("hidden GNU metadata substituted for missing archive end blocks")
	}
}

// Transport success alone must never grant the future sanitation stage a
// complete-backup claim: archive policy verification is a distinct gate.
func TestBootstrapArchivePipelineRequiresTransportAndArchivePolicy(t *testing.T) {
	valid := bootstrapSanitationArchive(t, nil)
	cases := []struct {
		name                       string
		payload                    []byte
		end                        bool
		wantTransport, wantArchive bool
	}{
		{"valid", valid, true, true, true},
		{"truncated transport", valid, false, false, false},
		{"malformed archive", []byte("not a tar archive"), true, true, false},
		{"appended archive payload", append(append([]byte(nil), valid...), []byte("unexpected")...), true, true, false},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			encoded := base64.StdEncoding.EncodeToString(tt.payload)
			var stream strings.Builder
			stream.WriteString("P28_BEGIN_TEST\n")
			for len(encoded) > 76 {
				stream.WriteString(encoded[:76] + "\n")
				encoded = encoded[76:]
			}
			if encoded != "" {
				stream.WriteString(encoded + "\n")
			}
			if tt.end {
				stream.WriteString("P28_END_TEST\n")
			}
			var destination bytes.Buffer
			result, err := decodeBootstrapArchiveStream(strings.NewReader(stream.String()), &destination, "P28_BEGIN_TEST", "P28_END_TEST", 1<<20)
			if (err == nil) != tt.wantTransport {
				t.Fatalf("transport success=%v want=%v", err == nil, tt.wantTransport)
			}
			if err != nil {
				if result.SHA256 != "" {
					t.Fatal("failed export returned completion evidence")
				}
				return
			}
			archiveErr := validateBootstrapSanitationArchive(bytes.NewReader(destination.Bytes()), bootstrapArchivePolicyTLS)
			if (archiveErr == nil) != tt.wantArchive {
				t.Fatalf("archive success=%v want=%v", archiveErr == nil, tt.wantArchive)
			}
		})
	}
}
