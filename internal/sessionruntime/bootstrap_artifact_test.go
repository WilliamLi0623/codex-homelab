package sessionruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/WilliamLi0623/codex-homelab/internal/store"
)

func TestBootstrapArtifactVerifierAcceptsOnlyCompletePinnedBundle(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("artifact owner/mode verification is Linux-specific")
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	manifest := testArtifactBundle(t, root)
	evidence, err := verifyBootstrapArtifactBundle(context.Background(), root, manifest)
	if err != nil || !validBootstrapEvidence(evidence) {
		t.Fatalf("verify bundle evidence=%+v err=%v", evidence, err)
	}
	if _, err := verifyBootstrapArtifactBundle(context.Background(), root, manifest); err != nil {
		t.Fatalf("repeat read-only verification: %v", err)
	}
}

func TestBootstrapArtifactVerifierHonorsCancellation(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("artifact owner/mode verification is Linux-specific")
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	manifest := testArtifactBundle(t, root)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if evidence, err := verifyBootstrapArtifactBundle(ctx, root, manifest); err == nil || evidence != (store.SessionBootstrapEvidence{}) {
		t.Fatalf("canceled verifier evidence=%+v err=%v", evidence, err)
	}
}

func TestBootstrapArtifactVerifierRejectsIncompleteTamperedAndExtraFiles(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("artifact owner/mode verification is Linux-specific")
	}
	for _, kind := range []string{"missing", "tampered", "extra", "symlink", "wrong mode", "wrong target", "unsafe manifest path"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Chmod(root, 0700); err != nil {
				t.Fatal(err)
			}
			manifest := testArtifactBundle(t, root)
			switch kind {
			case "missing":
				if err := os.Remove(filepath.Join(root, manifest.files[0].path)); err != nil {
					t.Fatal(err)
				}
			case "tampered":
				if err := os.WriteFile(filepath.Join(root, manifest.files[0].path), []byte("changed"), 0755); err != nil {
					t.Fatal(err)
				}
			case "extra":
				if err := os.WriteFile(filepath.Join(root, "unexpected"), []byte("extra"), 0644); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Remove(filepath.Join(root, manifest.files[0].path)); err != nil {
					t.Fatal(err)
				}
				outside := filepath.Join(t.TempDir(), "outside")
				if err := os.WriteFile(outside, []byte("outside"), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, filepath.Join(root, manifest.files[0].path)); err != nil {
					t.Fatal(err)
				}
			case "wrong mode":
				if err := os.Chmod(filepath.Join(root, manifest.files[0].path), 0644); err != nil {
					t.Fatal(err)
				}
			case "wrong target":
				manifest.target = "aarch64-unknown-linux-musl"
			case "unsafe manifest path":
				manifest.files[0].path = "../outside"
			}
			if evidence, err := verifyBootstrapArtifactBundle(context.Background(), root, manifest); err == nil || evidence != (store.SessionBootstrapEvidence{}) {
				t.Fatalf("unsafe artifact accepted evidence=%+v err=%v", evidence, err)
			}
		})
	}
}

func TestBootstrapCodex0155BundlePinsObservedLXC3006Files(t *testing.T) {
	if bootstrapCodex0155Bundle.version != "0.155.0" || bootstrapCodex0155Bundle.target != "x86_64-unknown-linux-musl" || len(bootstrapCodex0155Bundle.files) != 44 {
		t.Fatalf("bundle identity = %q/%q with %d files", bootstrapCodex0155Bundle.version, bootstrapCodex0155Bundle.target, len(bootstrapCodex0155Bundle.files))
	}
	for _, file := range bootstrapCodex0155Bundle.files {
		if len(file.sha256) != 64 || strings.TrimSpace(file.path) != file.path {
			t.Errorf("invalid pinned artifact entry %+v", file)
		}
	}
	if got := bootstrapCodex0155Bundle.files[0].sha256; got != "660e159a49e823ac8e5986cb238f73158ce4b957d40d9292f8de90862644b501" {
		t.Fatalf("codex binary hash = %s", got)
	}
}

func testArtifactBundle(t *testing.T, root string) codexArtifactBundle {
	t.Helper()
	files := []codexArtifactFile{{path: "bin/codex", mode: 0755}, {path: "data/package.json", mode: 0644}}
	contents := [][]byte{[]byte("harmless executable fixture"), []byte("fixture metadata")}
	for index := range files {
		path := filepath.Join(root, filepath.FromSlash(files[index].path))
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		mode := files[index].mode
		if err := os.WriteFile(path, contents[index], mode); err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(contents[index])
		files[index].sha256 = hex.EncodeToString(digest[:])
	}
	return codexArtifactBundle{version: "fixture", target: "x86_64-unknown-linux-musl", files: files}
}
