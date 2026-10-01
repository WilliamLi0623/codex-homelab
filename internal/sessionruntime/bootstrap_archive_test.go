package sessionruntime

import (
	"archive/tar"
	"bytes"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
)

func TestBootstrapK3sCredentialArchivePolicyMatchesAuditedExactPaths(t *testing.T) {
	policy := bootstrapArchivePolicy(2)
	roots, ok := bootstrapArchivePolicyRoots(policy)
	want := []string{
		"etc/rancher/node/password",
		"etc/systemd/system/k3s-agent.service.env",
		"var/lib/rancher/k3s/agent/client-ca.crt",
		"var/lib/rancher/k3s/agent/client-k3s-controller.crt",
		"var/lib/rancher/k3s/agent/client-k3s-controller.key",
		"var/lib/rancher/k3s/agent/client-kube-proxy.crt",
		"var/lib/rancher/k3s/agent/client-kube-proxy.key",
		"var/lib/rancher/k3s/agent/client-kubelet.crt",
		"var/lib/rancher/k3s/agent/client-kubelet.key",
		"var/lib/rancher/k3s/agent/k3scontroller.kubeconfig",
		"var/lib/rancher/k3s/agent/kubelet.kubeconfig",
		"var/lib/rancher/k3s/agent/kubeproxy.kubeconfig",
		"var/lib/rancher/k3s/agent/server-ca.crt",
		"var/lib/rancher/k3s/agent/serving-kubelet.crt",
		"var/lib/rancher/k3s/agent/serving-kubelet.key",
	}
	if !ok || !reflect.DeepEqual(roots, want) {
		t.Fatalf("K3s archive roots = %#v, ok=%v; want audited exact path allowlist", roots, ok)
	}
	archive := bootstrapArchiveFromRoots(t, roots)
	if err := validateBootstrapSanitationArchive(bytes.NewReader(archive), policy); err != nil {
		t.Fatalf("valid audited K3s credential archive rejected: %v", err)
	}
	if err := validateBootstrapSanitationArchive(bytes.NewReader(archive), bootstrapArchivePolicyTLS); err == nil {
		t.Fatal("K3s credential archive accepted under the TLS-only policy")
	}
}

func TestBootstrapSessionImageArchivePolicyV2AddsAuditedSSHHostKeysWithoutChangingV1(t *testing.T) {
	v1, ok := bootstrapArchivePolicyRoots(bootstrapArchivePolicySessionImageCredentials)
	if !ok || len(v1) != 17 {
		t.Fatalf("v1 roots = %d, valid=%v; want stable 17-root policy", len(v1), ok)
	}
	v2, ok := bootstrapArchivePolicyRoots(bootstrapArchivePolicySessionImageCredentialsV2)
	if !ok || len(v2) != 23 {
		t.Fatalf("v2 roots = %d, valid=%v; want 23-root policy", len(v2), ok)
	}
	if !reflect.DeepEqual(v2[:len(v1)], v1) {
		t.Fatal("v2 changed the existing v1 root sequence")
	}
	wantSSH := []string{
		"etc/ssh/ssh_host_rsa_key",
		"etc/ssh/ssh_host_rsa_key.pub",
		"etc/ssh/ssh_host_ecdsa_key",
		"etc/ssh/ssh_host_ecdsa_key.pub",
		"etc/ssh/ssh_host_ed25519_key",
		"etc/ssh/ssh_host_ed25519_key.pub",
	}
	if !reflect.DeepEqual(v2[len(v1):], wantSSH) {
		t.Fatalf("v2 SSH roots = %q, want %q", v2[len(v1):], wantSSH)
	}
	if name, ok := bootstrapArchivePolicyName(bootstrapArchivePolicySessionImageCredentialsV2); !ok || name != "session-image-credentials-v2" {
		t.Fatalf("v2 policy identity = %q, valid=%v", name, ok)
	}
}

func bootstrapArchiveFromRoots(t *testing.T, roots []string) []byte {
	t.Helper()
	var output bytes.Buffer
	writer := tar.NewWriter(&output)
	for _, name := range roots {
		if err := writer.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0600, Size: 1, Format: tar.FormatUSTAR}); err != nil {
			t.Fatalf("write audited archive member: %v", err)
		}
		if _, err := io.WriteString(writer, "x"); err != nil {
			t.Fatalf("write audited archive body: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close audited archive: %v", err)
	}
	return output.Bytes()
}

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
