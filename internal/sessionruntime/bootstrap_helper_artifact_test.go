package sessionruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/WilliamLi0623/codex-homelab/internal/store"
)

// These tests catch accepting changed bytes or unsafe cache paths before the
// Controller sends a trusted root helper to a generation-pinned guest.
func TestBootstrapHelperArtifactVerifiesExactBytesBeforeTransfer(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("helper filesystem trust is Linux-specific")
	}
	root := t.TempDir()
	file := filepath.Join(root, "helper")
	data := []byte("isolated helper fixture")
	if err := os.WriteFile(file, data, 0700); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(data)
	pin := hex.EncodeToString(hash[:])
	artifact, err := verifyBootstrapHelperArtifact(context.Background(), file, pin)
	if err != nil || string(artifact.Bytes) != string(data) || artifact.SHA256 != pin {
		t.Fatalf("verified artifact=%+v err=%v", artifact, err)
	}
	if err := os.WriteFile(file, []byte("changed fixture"), 0700); err != nil {
		t.Fatal(err)
	}
	if got, err := verifyBootstrapHelperArtifact(context.Background(), file, pin); err == nil || len(got.Bytes) != 0 {
		t.Fatal("changed helper bytes were accepted")
	}
}

func TestBootstrapHelperDigestBindsRuntimeGenerationAndVerifiedBytes(t *testing.T) {
	binding := store.SessionRuntimeBinding{ID: "binding", SessionID: "session", EpochID: "epoch", VMID: 4002, Generation: "first"}
	data := []byte("verified helper fixture")
	hash := sha256.Sum256(data)
	artifact := bootstrapHelperArtifact{Bytes: data, SHA256: hex.EncodeToString(hash[:])}
	first := bootstrapHelperGenerationDigest(binding, artifact)
	if !bootstrapDigestPattern.MatchString(first) {
		t.Fatal("valid helper binding produced no digest")
	}
	binding.Generation = "second"
	if first == bootstrapHelperGenerationDigest(binding, artifact) {
		t.Fatal("helper evidence lost runtime generation")
	}
	binding.Generation = "first"
	binding.VMID = 4003
	if first == bootstrapHelperGenerationDigest(binding, artifact) {
		t.Fatal("helper evidence lost VMID")
	}
	binding.VMID = 4002
	artifact.Bytes = []byte("unverified changed bytes")
	if bootstrapHelperGenerationDigest(binding, artifact) != "" {
		t.Fatal("unverified bytes acquired a helper binding digest")
	}
	if bootstrapHelperGenerationDigest(store.SessionRuntimeBinding{}, artifact) != "" {
		t.Fatal("invalid runtime binding accepted")
	}
}

func TestBootstrapHelperArtifactRejectsUnsafeModesLinksAndCancellation(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("helper filesystem trust is Linux-specific")
	}
	for _, kind := range []string{"mode", "symlink", "parent", "empty", "large", "pin", "cancel"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			file := filepath.Join(root, "helper")
			data := []byte("helper fixture")
			if err := os.WriteFile(file, data, 0700); err != nil {
				t.Fatal(err)
			}
			hash := sha256.Sum256(data)
			pin := hex.EncodeToString(hash[:])
			ctx := context.Background()
			switch kind {
			case "mode":
				if err := os.Chmod(file, 0770); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				alias := filepath.Join(root, "alias")
				if err := os.Symlink(file, alias); err != nil {
					t.Fatal(err)
				}
				file = alias
			case "parent":
				if err := os.Chmod(root, 0777); err != nil {
					t.Fatal(err)
				}
			case "empty":
				if err := os.Truncate(file, 0); err != nil {
					t.Fatal(err)
				}
			case "large":
				if err := os.Truncate(file, 32*1024*1024+1); err != nil {
					t.Fatal(err)
				}
			case "pin":
				pin = "invalid"
			case "cancel":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if got, err := verifyBootstrapHelperArtifact(ctx, file, pin); err == nil || len(got.Bytes) != 0 {
				t.Fatalf("unsafe %s accepted", kind)
			}
		})
	}
}

func TestBootstrapHelperArtifactLiveCacheWhenProvided(t *testing.T) {
	file, pin := os.Getenv("P28_HELPER_ARTIFACT_PATH"), os.Getenv("P28_HELPER_ARTIFACT_SHA256")
	if file == "" && pin == "" {
		t.Skip("opt-in trusted helper cache verification")
	}
	artifact, err := verifyBootstrapHelperArtifact(context.Background(), file, pin)
	if err != nil || len(artifact.Bytes) == 0 || artifact.SHA256 != pin {
		t.Fatalf("live helper verification failed: %v", err)
	}
}
