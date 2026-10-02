package sessionruntime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestBootstrapCodexInstallPromotesOnlyVerifiedBundle(t *testing.T) {
	requireLinuxRoot(t)
	root := t.TempDir()
	installRoot := filepath.Join(root, "opt", "codex")
	if err := os.MkdirAll(installRoot, 0700); err != nil {
		t.Fatal(err)
	}
	launcherDir := filepath.Join(root, "usr", "local", "bin")
	if err := os.MkdirAll(launcherDir, 0700); err != nil {
		t.Fatal(err)
	}
	manifest, entries := writeTransferFixture(t, filepath.Join(root, "source"))
	archive := makeArtifactTar(t, filepath.Join(root, "source"), manifest, entries)
	digest := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	launcher := filepath.Join(launcherDir, "codex")
	evidence, err := installBootstrapCodexBundleAt(context.Background(), bytes.NewReader(frameArtifactTar(archive, true)), digest, manifest, installRoot, launcher)
	if err != nil || !validBootstrapEvidence(evidence) {
		t.Fatalf("install evidence=%+v err=%v", evidence, err)
	}
	observed, verified, err := observeBootstrapCodexBundleAt(context.Background(), digest, manifest, installRoot, launcher)
	if err != nil || !verified || observed != evidence {
		t.Fatalf("read-only observe evidence=%+v verified=%v err=%v", observed, verified, err)
	}
	installed := filepath.Join(installRoot, digest)
	for _, entry := range entries {
		path := filepath.Join(installed, filepath.FromSlash(entry.Path))
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read installed %q: %v", entry.Path, err)
		}
		if int64(len(got)) != entry.Size || sha256Hex(got) != entry.SHA256 {
			t.Fatalf("installed %q does not match manifest", entry.Path)
		}
	}
	link, err := os.Readlink(launcher)
	if err != nil || link != filepath.Join(installed, "bin", "codex") {
		t.Fatalf("launcher link=%q err=%v", link, err)
	}
}

func TestBootstrapCodexInstallRejectsExistingTargetsWithoutReplacing(t *testing.T) {
	requireLinuxRoot(t)
	root := t.TempDir()
	installRoot := filepath.Join(root, "opt", "codex")
	launcherDir := filepath.Join(root, "usr", "local", "bin")
	if err := os.MkdirAll(installRoot, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(launcherDir, 0700); err != nil {
		t.Fatal(err)
	}
	manifest, entries := writeTransferFixture(t, filepath.Join(root, "source"))
	archive := makeArtifactTar(t, filepath.Join(root, "source"), manifest, entries)
	launcher := filepath.Join(launcherDir, "codex")
	if err := os.WriteFile(launcher, []byte("keep existing"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := installBootstrapCodexBundleAt(context.Background(), bytes.NewReader(frameArtifactTar(archive, true)), "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", manifest, installRoot, launcher)
	if err == nil {
		t.Fatal("existing launcher was overwritten")
	}
	got, readErr := os.ReadFile(launcher)
	if readErr != nil || string(got) != "keep existing" {
		t.Fatalf("existing launcher changed to %q, err=%v", got, readErr)
	}
}

func TestBootstrapCodexInstallLeavesPartialStateAndNeverRepairsIt(t *testing.T) {
	requireLinuxRoot(t)
	root := t.TempDir()
	installRoot := filepath.Join(root, "opt", "codex")
	launcherDir := filepath.Join(root, "usr", "local", "bin")
	if err := os.MkdirAll(installRoot, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(launcherDir, 0700); err != nil {
		t.Fatal(err)
	}
	manifest, entries := writeTransferFixture(t, filepath.Join(root, "source"))
	archive := makeArtifactTar(t, filepath.Join(root, "source"), manifest, entries)
	digest := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	launcher := filepath.Join(launcherDir, "codex")
	partial := frameArtifactTar(archive, false)
	if _, err := installBootstrapCodexBundleAt(context.Background(), bytes.NewReader(partial), digest, manifest, installRoot, launcher); err == nil {
		t.Fatal("missing commit marker was accepted")
	}
	stage := filepath.Join(installRoot, ".staging-"+digest)
	if info, err := os.Lstat(stage); err != nil || !info.IsDir() {
		t.Fatalf("partial stage was not retained: info=%v err=%v", info, err)
	}
	if _, err := installBootstrapCodexBundleAt(context.Background(), bytes.NewReader(frameArtifactTar(archive, true)), digest, manifest, installRoot, launcher); err == nil {
		t.Fatal("existing partial stage was automatically repaired/retried")
	}
	if _, err := os.Lstat(filepath.Join(installRoot, digest)); !os.IsNotExist(err) {
		t.Fatalf("partial stage promoted unexpectedly: %v", err)
	}
}

func TestBootstrapCodexInstallRejectsArchiveContentTampering(t *testing.T) {
	requireLinuxRoot(t)
	root := t.TempDir()
	installRoot := filepath.Join(root, "opt", "codex")
	launcherDir := filepath.Join(root, "usr", "local", "bin")
	if err := os.MkdirAll(installRoot, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(launcherDir, 0700); err != nil {
		t.Fatal(err)
	}
	manifest, entries := writeTransferFixture(t, filepath.Join(root, "source"))
	archive := makeArtifactTar(t, filepath.Join(root, "source"), manifest, entries)
	archive = bytes.Replace(archive, []byte("unicode fixture"), []byte("unicode exploit"), 1)
	_, err := installBootstrapCodexBundleAt(context.Background(), bytes.NewReader(frameArtifactTar(archive, true)), "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", manifest, installRoot, filepath.Join(launcherDir, "codex"))
	if err == nil {
		t.Fatal("tampered archive was installed")
	}
	if _, err := os.Lstat(filepath.Join(installRoot, "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")); !os.IsNotExist(err) {
		t.Fatalf("tampered archive was promoted: %v", err)
	}
}

func requireLinuxRoot(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("guest installer validation is Linux-specific")
	}
	if os.Geteuid() != 0 {
		t.Skip("guest installer runs as root")
	}
}

func makeArtifactTar(t *testing.T, root string, bundle codexArtifactBundle, entries []verifiedCodexArtifact) []byte {
	t.Helper()
	var archive bytes.Buffer
	if err := writeBootstrapArtifactTarFiles(context.Background(), root, bundle, entries, &archive); err != nil {
		t.Fatal(err)
	}
	return archive.Bytes()
}

func frameArtifactTar(archive []byte, commit bool) []byte {
	frame := make([]byte, 0, len(bootstrapArtifactTransferMagic)+8+sha256.Size+len(archive)+sha256.Size+len(bootstrapArtifactCommitMagic))
	frame = append(frame, bootstrapArtifactTransferMagic...)
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(archive)))
	frame = append(frame, length[:]...)
	digest := sha256.Sum256(archive)
	frame = append(frame, digest[:]...)
	frame = append(frame, archive...)
	frame = append(frame, digest[:]...)
	if commit {
		frame = append(frame, bootstrapArtifactCommitMagic...)
	}
	return frame
}

func sha256Hex(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}
