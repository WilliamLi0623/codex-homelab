package sessionruntime

import (
	"archive/tar"
	"bytes"
	"fmt"
	"io"
	"strings"
	"testing"
)

func TestValidateBootstrapSanitationArchivePoliciesAreIndependent(t *testing.T) {
	archive := bootstrapSanitationArchive(t, nil)
	if err := validateBootstrapSanitationArchive(bytes.NewReader(archive), bootstrapArchivePolicyTLS); err != nil {
		t.Fatalf("valid TLS sanitation archive rejected: %v", err)
	}
}

func TestValidateBootstrapSanitationArchiveRejectsUnsafeMembers(t *testing.T) {
	tests := []struct {
		name     string
		entries  []tarEntryFixture
		trailer  []byte
		omitRoot string
	}{
		{name: "absolute path", entries: []tarEntryFixture{{name: "/etc/ssl/private/ssl-cert-snakeoil.key", typeflag: tar.TypeReg}}},
		{name: "traversal path", entries: []tarEntryFixture{{name: "etc/ssl/private/../private/ssl-cert-snakeoil.key", typeflag: tar.TypeReg}}},
		{name: "duplicate path", entries: []tarEntryFixture{{name: "etc/ssl/private/ssl-cert-snakeoil.key", typeflag: tar.TypeReg}}},
		{name: "special device", entries: []tarEntryFixture{{name: "etc/ssl/private/ssl-cert-snakeoil.key", typeflag: tar.TypeChar}}, omitRoot: "etc/ssl/private/ssl-cert-snakeoil.key"},
		{name: "key directory", entries: []tarEntryFixture{{name: "etc/ssl/private/ssl-cert-snakeoil.key", typeflag: tar.TypeDir}}, omitRoot: "etc/ssl/private/ssl-cert-snakeoil.key"},
		{name: "key symlink", entries: []tarEntryFixture{{name: "etc/ssl/private/ssl-cert-snakeoil.key", typeflag: tar.TypeSymlink, linkname: "ssl-cert-snakeoil.pem"}}, omitRoot: "etc/ssl/private/ssl-cert-snakeoil.key"},
		{name: "escaping symlink", entries: []tarEntryFixture{{name: "etc/ssl/private/ssl-cert-snakeoil.key", typeflag: tar.TypeSymlink, linkname: "../../outside"}}, omitRoot: "etc/ssl/private/ssl-cert-snakeoil.key"},
		{name: "child cannot represent exact root", entries: []tarEntryFixture{{name: "etc/ssl/private/ssl-cert-snakeoil.key/child", typeflag: tar.TypeReg}}},
		{name: "trailing slash cannot represent TLS file root", entries: []tarEntryFixture{{name: "etc/ssl/private/ssl-cert-snakeoil.key", typeflag: tar.TypeReg, trailingSlash: true}}, omitRoot: "etc/ssl/private/ssl-cert-snakeoil.key"},
		{name: "missing intended root", entries: nil, omitRoot: "etc/ssl/certs/ssl-cert-snakeoil.pem"},
		{name: "appended payload", entries: nil, trailer: []byte("unexpected payload")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			archive := bootstrapSanitationArchive(t, tt.entries, tt.omitRoot)
			archive = append(archive, tt.trailer...)
			if tt.name == "trailing slash cannot represent TLS file root" && !hasTarMember(archive, "etc/ssl/private/ssl-cert-snakeoil.key/", tar.TypeReg) {
				t.Fatal("fixture did not preserve slash-suffixed member name")
			}
			err := validateBootstrapSanitationArchive(bytes.NewReader(archive), bootstrapArchivePolicyTLS)
			if err == nil {
				t.Fatal("unsafe or incomplete archive accepted")
			}
			if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "outside") || strings.Contains(err.Error(), "ssl-cert") {
				t.Fatalf("archive error disclosed member data: %v", err)
			}
		})
	}
}

func hasTarMember(archive []byte, want string, typeflag byte) bool {
	reader := tar.NewReader(bytes.NewReader(archive))
	for {
		header, err := reader.Next()
		if err != nil {
			return false
		}
		if header.Name == want && header.Typeflag == typeflag {
			return true
		}
	}
}

func TestValidateBootstrapSanitationArchiveAllowsZeroPadding(t *testing.T) {
	archive := bootstrapSanitationArchive(t, nil)
	archive = append(archive, make([]byte, 1024)...)
	if err := validateBootstrapSanitationArchive(bytes.NewReader(archive), bootstrapArchivePolicyTLS); err != nil {
		t.Fatalf("valid zero padding rejected: %v", err)
	}
}

func TestValidateBootstrapSanitationArchiveRequiresTwoEndBlocks(t *testing.T) {
	archive := bootstrapSanitationArchive(t, nil)
	for _, test := range []struct {
		name string
		trim int
	}{{name: "one block missing", trim: 512}, {name: "both blocks missing", trim: 1024}} {
		t.Run(test.name, func(t *testing.T) {
			truncated := archive[:len(archive)-test.trim]
			if err := validateBootstrapSanitationArchive(bytes.NewReader(truncated), bootstrapArchivePolicyTLS); err == nil {
				t.Fatal("archive without two end blocks accepted")
			}
		})
	}
}

func TestValidateBootstrapSanitationArchiveRejectsPendingTarMetadata(t *testing.T) {
	base := bootstrapSanitationArchive(t, nil)
	base = base[:len(base)-1024]
	for _, test := range []struct {
		name   string
		format tar.Format
	}{
		{name: "GNU long name", format: tar.FormatGNU},
		{name: "PAX extended header", format: tar.FormatPAX},
	} {
		t.Run(test.name, func(t *testing.T) {
			archive := append(append([]byte(nil), base...), pendingTarMetadata(t, test.format)...)
			if err := validateBootstrapSanitationArchive(bytes.NewReader(archive), bootstrapArchivePolicyTLS); err == nil {
				t.Fatal("archive with pending tar metadata and no end markers accepted")
			}
		})
	}
}

func pendingTarMetadata(t *testing.T, format tar.Format) []byte {
	t.Helper()
	var output bytes.Buffer
	writer := tar.NewWriter(&output)
	if err := writer.WriteHeader(&tar.Header{Name: strings.Repeat("a", 120), Typeflag: tar.TypeReg, Mode: 0600, Format: format}); err != nil {
		t.Fatalf("write metadata fixture: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close metadata fixture: %v", err)
	}
	return output.Bytes()[:1024]
}

func TestValidateBootstrapSanitationArchiveEnforcesLimits(t *testing.T) {
	archive := bootstrapSanitationArchive(t, nil)
	for _, limits := range []bootstrapArchiveLimits{
		{maxBytes: int64(len(archive) - 1), maxEntries: 100, maxPathBytes: 4096},
		{maxBytes: int64(len(archive)), maxEntries: 1, maxPathBytes: 4096},
		{maxBytes: int64(len(archive)), maxEntries: 100, maxPathBytes: 8},
	} {
		if err := validateBootstrapSanitationArchiveWithLimits(bytes.NewReader(archive), bootstrapArchivePolicyTLS, limits); err == nil {
			t.Fatalf("archive accepted with limits %+v", limits)
		}
	}
}

func TestValidateBootstrapSanitationArchiveRejectsUnknownPolicy(t *testing.T) {
	archive := bootstrapSanitationArchive(t, nil)
	if err := validateBootstrapSanitationArchive(bytes.NewReader(archive), bootstrapArchivePolicy(255)); err == nil {
		t.Fatal("unknown archive policy accepted")
	}
}

type tarEntryFixture struct {
	name          string
	typeflag      byte
	linkname      string
	trailingSlash bool
}

func bootstrapSanitationArchive(t *testing.T, extra []tarEntryFixture, omitted ...string) []byte {
	t.Helper()
	var output bytes.Buffer
	writer := tar.NewWriter(&output)
	baseline := []tarEntryFixture{
		{name: "etc/ssl/private/ssl-cert-snakeoil.key", typeflag: tar.TypeReg},
		{name: "etc/ssl/certs/ssl-cert-snakeoil.pem", typeflag: tar.TypeReg},
	}
	var trailingSlashHeaderOffsets []int
	write := func(entry tarEntryFixture) {
		t.Helper()
		header := &tar.Header{Name: entry.name, Typeflag: entry.typeflag, Linkname: entry.linkname, Mode: 0600, Format: tar.FormatUSTAR}
		if entry.typeflag == tar.TypeReg || entry.typeflag == tar.TypeRegA {
			header.Size = 1
		}
		if err := writer.WriteHeader(header); err != nil {
			t.Fatalf("write fixture header: %v", err)
		}
		if entry.trailingSlash {
			trailingSlashHeaderOffsets = append(trailingSlashHeaderOffsets, output.Len()-512)
		}
		if header.Size > 0 {
			if _, err := io.WriteString(writer, "x"); err != nil {
				t.Fatalf("write fixture body: %v", err)
			}
		}
	}
	for _, entry := range baseline {
		if len(omitted) > 0 && omitted[0] == entry.name {
			continue
		}
		write(entry)
	}
	for _, entry := range extra {
		write(entry)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close fixture archive: %v", err)
	}
	for _, offset := range trailingSlashHeaderOffsets {
		header := output.Bytes()[offset : offset+512]
		nameEnd := bytes.IndexByte(header[:100], 0)
		if nameEnd < 0 {
			t.Fatal("fixture name field has no terminator")
		}
		header[nameEnd] = '/'
		header[156] = tar.TypeReg
		for index := 148; index < 156; index++ {
			header[index] = ' '
		}
		var checksum int64
		for _, value := range header {
			checksum += int64(value)
		}
		copy(header[148:156], []byte(fmt.Sprintf("%06o\x00 ", checksum)))
	}
	return output.Bytes()
}
