package sessionruntime

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/WilliamLi0623/codex-homelab/internal/store"
)

func backupStoreFixture(t *testing.T) (*bootstrapBackupStore, store.SessionRuntimeBinding) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("private backup persistence requires Linux ownership checks")
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	registry, err := newBootstrapBackupStore(root)
	if err != nil {
		t.Fatal(err)
	}
	return registry, store.SessionRuntimeBinding{ID: "binding", SessionID: "session", EpochID: "epoch", Generation: "generation", VMID: 4002}
}

func TestBootstrapBackupStorePersistsReopensAndNeverReplays(t *testing.T) {
	registry, binding := backupStoreFixture(t)
	archive := bootstrapSanitationArchive(t, nil)
	calls := 0
	export := func(ctx context.Context, b store.SessionRuntimeBinding, w io.Writer) (bootstrapTLSArchiveEvidence, error) {
		calls++
		if b != binding {
			t.Fatal("export binding changed")
		}
		_, err := w.Write(archive)
		return bootstrapTLSArchiveEvidence{Bytes: int64(len(archive)), SHA256: materialDigest(archive), IsolationSHA256: strings.Repeat("a", 64)}, err
	}
	evidence, err := registry.save(context.Background(), binding, export)
	if err != nil || !validBootstrapEvidence(evidence) {
		t.Fatalf("save: %v", err)
	}
	reopened, err := newBootstrapBackupStore(registry.root)
	if err != nil {
		t.Fatal(err)
	}
	observed, ok, err := reopened.observe(context.Background(), binding)
	if err != nil || !ok || observed != evidence {
		t.Fatal("durable backup cannot reconcile after reopen")
	}
	if _, ok, err := reopened.observePolicy(context.Background(), binding, bootstrapArchivePolicySessionImageCredentialsV2); err == nil || ok {
		t.Fatal("TLS-only backup incorrectly satisfied the Session-image credential policy")
	}
	if _, err := registry.save(context.Background(), binding, export); err == nil || calls != 1 {
		t.Fatal("existing backup replayed")
	}
	file := filepath.Join(registry.directory(binding), "archive.tar")
	info, err := os.Stat(file)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("archive not private")
	}
	changed := binding
	changed.Generation = "different"
	if _, ok, err := registry.observe(context.Background(), changed); ok || err == nil {
		t.Fatal("wrong generation adopted backup")
	}
}

func TestBootstrapBackupStorePersistsCombinedSessionImageCredentialArchive(t *testing.T) {
	registry, binding := backupStoreFixture(t)
	if !strings.HasPrefix(filepath.Base(registry.directory(binding)), "tls-") {
		t.Fatal("existing backup storage namespace changed")
	}
	roots, ok := bootstrapArchivePolicyRoots(bootstrapArchivePolicySessionImageCredentialsV2)
	if !ok {
		t.Fatal("audited Session-image credential archive policy unavailable")
	}
	archive := bootstrapArchiveFromRoots(t, roots)
	export := func(ctx context.Context, b store.SessionRuntimeBinding, w io.Writer) (bootstrapTLSArchiveEvidence, error) {
		if b != binding {
			t.Fatal("credential archive binding changed")
		}
		_, err := w.Write(archive)
		return bootstrapTLSArchiveEvidence{Bytes: int64(len(archive)), SHA256: materialDigest(archive), IsolationSHA256: strings.Repeat("d", 64)}, err
	}
	evidence, err := registry.save(context.Background(), binding, export)
	if err != nil || !validBootstrapEvidence(evidence) {
		t.Fatalf("combined session image credential backup save: %v", err)
	}
	reopened, err := newBootstrapBackupStore(registry.root)
	if err != nil {
		t.Fatal(err)
	}
	observed, ok, err := reopened.observe(context.Background(), binding)
	if err != nil || !ok || observed != evidence {
		t.Fatal("combined session image credential backup cannot reconcile after reopen")
	}
	if policyEvidence, ok, err := reopened.observePolicy(context.Background(), binding, bootstrapArchivePolicySessionImageCredentialsV2); err != nil || !ok || policyEvidence != evidence {
		t.Fatalf("exact Session-image archive policy observation: ok=%v err=%v", ok, err)
	}
	manifestData, err := os.ReadFile(filepath.Join(registry.directory(binding), "complete.json"))
	if err != nil {
		t.Fatal("completed backup manifest missing")
	}
	var manifest bootstrapBackupManifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil || manifest.Policy != "session-image-credentials-v2" {
		t.Fatalf("completed archive policy = %q, error=%v", manifest.Policy, err)
	}
}

func TestBootstrapBackupStoreFailuresRemainAmbiguousAndUnrepaired(t *testing.T) {
	for _, kind := range []string{"export error", "wrong hash", "malformed archive"} {
		t.Run(kind, func(t *testing.T) {
			registry, binding := backupStoreFixture(t)
			archive := bootstrapSanitationArchive(t, nil)
			calls := 0
			export := func(ctx context.Context, b store.SessionRuntimeBinding, w io.Writer) (bootstrapTLSArchiveEvidence, error) {
				calls++
				if kind == "malformed archive" {
					archive = []byte("harmless invalid fixture")
				}
				_, err := w.Write(archive)
				result := bootstrapTLSArchiveEvidence{Bytes: int64(len(archive)), SHA256: materialDigest(archive), IsolationSHA256: strings.Repeat("a", 64)}
				if kind == "wrong hash" {
					result.SHA256 = strings.Repeat("b", 64)
				}
				if kind == "export error" {
					err = errors.New("secret transport detail")
				}
				return result, err
			}
			if _, err := registry.save(context.Background(), binding, export); err == nil || strings.Contains(err.Error(), "secret") {
				t.Fatal("failed backup accepted or error leaked")
			}
			if _, ok, err := registry.observe(context.Background(), binding); ok || err == nil {
				t.Fatal("partial backup observed as complete")
			}
			if _, err := registry.save(context.Background(), binding, export); err == nil || calls != 1 {
				t.Fatal("partial backup was replayed")
			}
			if _, err := os.Stat(filepath.Join(registry.directory(binding), "archive.tar")); err != nil {
				t.Fatal("partial backup silently deleted")
			}
		})
	}
}

func TestBootstrapBackupStoreObserveRejectsTamperingWithoutRepair(t *testing.T) {
	registry, binding := backupStoreFixture(t)
	archive := bootstrapSanitationArchive(t, nil)
	export := func(ctx context.Context, b store.SessionRuntimeBinding, w io.Writer) (bootstrapTLSArchiveEvidence, error) {
		_, err := w.Write(archive)
		return bootstrapTLSArchiveEvidence{Bytes: int64(len(archive)), SHA256: materialDigest(archive), IsolationSHA256: strings.Repeat("a", 64)}, err
	}
	if _, err := registry.save(context.Background(), binding, export); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(registry.directory(binding), "archive.tar")
	if err := os.Chmod(file, 0644); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := registry.observe(context.Background(), binding); ok || err == nil {
		t.Fatal("public archive accepted")
	}
	info, _ := os.Stat(file)
	if info.Mode().Perm() != 0644 {
		t.Fatal("observer repaired permission")
	}
}

func TestBootstrapBackupStoreRejectsChangedContentsAndCanceledSave(t *testing.T) {
	registry, binding := backupStoreFixture(t)
	archive := bootstrapSanitationArchive(t, nil)
	called := false
	export := func(ctx context.Context, b store.SessionRuntimeBinding, w io.Writer) (bootstrapTLSArchiveEvidence, error) {
		called = true
		_, err := w.Write(archive)
		return bootstrapTLSArchiveEvidence{Bytes: int64(len(archive)), SHA256: materialDigest(archive), IsolationSHA256: strings.Repeat("a", 64)}, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := registry.save(ctx, binding, export); err == nil || called {
		t.Fatal("canceled save performed export")
	}
	if _, err := os.Stat(registry.directory(binding)); !os.IsNotExist(err) {
		t.Fatal("canceled save created intent directory")
	}
	if _, err := registry.save(context.Background(), binding, export); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(registry.directory(binding), "archive.tar")
	tampered := append([]byte(nil), archive...)
	tampered[512] = 'z'
	if err := os.WriteFile(file, tampered, 0600); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := registry.observe(context.Background(), binding); ok || err == nil {
		t.Fatal("changed archive accepted")
	}
	actual, err := os.ReadFile(file)
	if err != nil || string(actual) != string(tampered) {
		t.Fatal("observer rewrote archive")
	}
}

func TestBootstrapBackupStoreRejectsSymlinkRootAndArchive(t *testing.T) {
	registry, binding := backupStoreFixture(t)
	alias := filepath.Join(t.TempDir(), "root-link")
	if err := os.Symlink(registry.root, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := newBootstrapBackupStore(alias); err == nil {
		t.Fatal("symlink backup root accepted")
	}
	dir := registry.directory(binding)
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(registry.root, "harmless-target")
	if err := os.WriteFile(target, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "archive.tar")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := verifyBootstrapBackupArchive(context.Background(), filepath.Join(dir, "archive.tar")); err == nil {
		t.Fatal("symlink archive followed")
	}
}
