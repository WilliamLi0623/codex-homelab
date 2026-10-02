package sessionruntime

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/WilliamLi0623/codex-homelab/internal/store"
)

func TestBootstrapArtifactTarIsDeterministicAndPreservesManifestFiles(t *testing.T) {
	root := t.TempDir()
	manifest, entries := writeTransferFixture(t, root)
	var first, second bytes.Buffer
	if err := writeBootstrapArtifactTarFiles(context.Background(), root, manifest, entries, &first); err != nil {
		t.Fatalf("write tar: %v", err)
	}
	if err := writeBootstrapArtifactTarFiles(context.Background(), root, manifest, entries, &second); err != nil {
		t.Fatalf("write repeat tar: %v", err)
	}
	if !bytes.Equal(first.Bytes(), second.Bytes()) {
		t.Fatal("same verified bundle produced different tar bytes")
	}

	reader := tar.NewReader(bytes.NewReader(first.Bytes()))
	manifestHeader, err := reader.Next()
	if err != nil || manifestHeader.Name != bootstrapArtifactManifestPath || manifestHeader.Mode != 0600 || manifestHeader.Typeflag != tar.TypeReg {
		t.Fatalf("manifest header = %+v, err=%v", manifestHeader, err)
	}
	manifestBytes, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	var embedded struct {
		Version string                  `json:"version"`
		Target  string                  `json:"target"`
		Files   []verifiedCodexArtifact `json:"files"`
	}
	if err := json.Unmarshal(manifestBytes, &embedded); err != nil || embedded.Version != "fixture" || embedded.Target != bootstrapCodex0155Target || len(embedded.Files) != 2 || embedded.Files[1].Path != "data/你好.txt" {
		t.Fatalf("embedded manifest = %+v, err=%v", embedded, err)
	}
	for index, want := range []struct {
		path string
		mode int64
		body string
	}{{"bin/codex", 0755, "executable fixture"}, {"data/你好.txt", 0644, "unicode fixture"}} {
		header, err := reader.Next()
		if err != nil {
			t.Fatalf("read entry %d: %v", index, err)
		}
		if header.Name != want.path || header.Mode != want.mode || header.Typeflag != tar.TypeReg || header.Uid != 0 || header.Gid != 0 || header.Size != int64(len(want.body)) {
			t.Fatalf("entry %d header = %+v", index, header)
		}
		body, err := io.ReadAll(reader)
		if err != nil || string(body) != want.body {
			t.Fatalf("entry %d body = %q, err=%v", index, body, err)
		}
	}
	if _, err := reader.Next(); err != io.EOF {
		t.Fatalf("unexpected extra tar entry: %v", err)
	}
}

func TestBootstrapArtifactTarRejectsChangedFileBeforeReportingSuccess(t *testing.T) {
	root := t.TempDir()
	manifest, entries := writeTransferFixture(t, root)
	if err := os.WriteFile(filepath.Join(root, "bin", "codex"), []byte("changed executable"), 0755); err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	if err := writeBootstrapArtifactTarFiles(context.Background(), root, manifest, entries, &archive); err == nil {
		t.Fatal("changed file was accepted against verified manifest")
	}
}

func TestBootstrapArtifactTarRejectsUnsafeManifestAndCancellation(t *testing.T) {
	root := t.TempDir()
	manifest, entries := writeTransferFixture(t, root)
	unsafe := manifest
	unsafe.files = append([]codexArtifactFile(nil), manifest.files...)
	unsafe.files[0].path = "../outside"
	outside := filepath.Join(filepath.Dir(root), "outside")
	if err := os.WriteFile(outside, []byte("must not escape root"), 0755); err != nil {
		t.Fatal(err)
	}
	unsafeEntries := append([]verifiedCodexArtifact(nil), entries...)
	unsafeEntries[0].Path = "../outside"
	var archive bytes.Buffer
	if err := writeBootstrapArtifactTarFiles(context.Background(), root, unsafe, unsafeEntries, &archive); err == nil || archive.Len() != 0 {
		t.Fatal("path traversal manifest was accepted")
	}
	duplicate := manifest
	duplicate.files = append([]codexArtifactFile(nil), manifest.files...)
	duplicate.files[1] = duplicate.files[0]
	if err := writeBootstrapArtifactTarFiles(context.Background(), root, duplicate, entries, &archive); err == nil {
		t.Fatal("duplicate manifest path was accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := writeBootstrapArtifactTarFiles(ctx, root, manifest, entries, &archive); err == nil {
		t.Fatal("canceled stream was accepted")
	}
}

func TestBootstrapArtifactTransferCommitsOnlyAfterSecondVerifiedPass(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("source ownership and mode verification is Linux-specific")
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	manifest, _ := writeTransferFixture(t, root)
	evidence, err := verifyBootstrapArtifactBundle(context.Background(), root, manifest)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := writeBootstrapArtifactTransfer(context.Background(), root, manifest, evidence, &output); err != nil {
		t.Fatalf("write framed transfer: %v", err)
	}
	if !bytes.HasPrefix(output.Bytes(), []byte(bootstrapArtifactTransferMagic)) || !bytes.HasSuffix(output.Bytes(), []byte(bootstrapArtifactCommitMagic)) {
		t.Fatal("transfer is missing framing or final commit marker")
	}

	mutating := &bootstrapArtifactMutatingWriter{target: filepath.Join(root, "bin", "codex")}
	if err := writeBootstrapArtifactTransfer(context.Background(), root, manifest, evidence, mutating); err == nil {
		t.Fatal("source mutation between passes was accepted")
	}
	if bytes.HasSuffix(mutating.Bytes(), []byte(bootstrapArtifactCommitMagic)) {
		t.Fatal("failed transfer emitted the commit marker")
	}
}

func TestBootstrapArtifactInstallDigestBindsGenerationAndVerifiedBundle(t *testing.T) {
	binding := store.SessionRuntimeBinding{ID: "binding-1", SessionID: "session-1", EpochID: "epoch-1", Generation: "generation-1", VMID: 4001}
	evidence := store.SessionBootstrapEvidence{SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	digest := bootstrapCodexGenerationArtifactDigest(binding, bootstrapCodex0155Bundle, evidence)
	if len(digest) != 64 || digest != bootstrapCodexGenerationArtifactDigest(binding, bootstrapCodex0155Bundle, evidence) {
		t.Fatalf("generation artifact digest is not stable lowercase SHA256: %q", digest)
	}
	changed := binding
	changed.Generation = "generation-2"
	if digest == bootstrapCodexGenerationArtifactDigest(changed, bootstrapCodex0155Bundle, evidence) {
		t.Fatal("generation artifact digest did not change with generation")
	}
	changedEvidence := evidence
	changedEvidence.SHA256 = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if digest == bootstrapCodexGenerationArtifactDigest(binding, bootstrapCodex0155Bundle, changedEvidence) {
		t.Fatal("generation artifact digest did not change with verified artifact")
	}
}

type bootstrapArtifactMutatingWriter struct {
	bytes.Buffer
	target  string
	mutated bool
}

func (w *bootstrapArtifactMutatingWriter) Write(data []byte) (int, error) {
	if !w.mutated && bytes.HasPrefix(data, []byte(bootstrapArtifactTransferMagic)) {
		w.mutated = true
		if err := os.WriteFile(w.target, []byte("mutation after preflight"), 0755); err != nil {
			return 0, err
		}
	}
	return w.Buffer.Write(data)
}

func writeTransferFixture(t *testing.T, root string) (codexArtifactBundle, []verifiedCodexArtifact) {
	t.Helper()
	contents := []struct {
		path string
		mode os.FileMode
		body []byte
	}{{"bin/codex", 0755, []byte("executable fixture")}, {"data/你好.txt", 0644, []byte("unicode fixture")}}
	manifest := codexArtifactBundle{version: "fixture", target: bootstrapCodex0155Target}
	entries := make([]verifiedCodexArtifact, 0, len(contents))
	for _, item := range contents {
		fullPath := filepath.Join(root, filepath.FromSlash(item.path))
		if err := os.MkdirAll(filepath.Dir(fullPath), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fullPath, item.body, item.mode); err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(item.body)
		hexDigest := hex.EncodeToString(digest[:])
		manifest.files = append(manifest.files, codexArtifactFile{path: item.path, sha256: hexDigest, mode: item.mode})
		entries = append(entries, verifiedCodexArtifact{Path: item.path, SHA256: hexDigest, Size: int64(len(item.body)), Mode: uint32(item.mode.Perm())})
	}
	return manifest, entries
}
