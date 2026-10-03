package sessionruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"github.com/WilliamLi0623/codex-homelab/internal/store"
)

const bootstrapHelperMaxBytes = 32 * 1024 * 1024

var errBootstrapHelperArtifact = errors.New("bootstrap helper artifact could not be verified")

type bootstrapHelperArtifact struct {
	Bytes  []byte
	SHA256 string
}

func bootstrapHelperGenerationDigest(binding store.SessionRuntimeBinding, artifact bootstrapHelperArtifact) string {
	if !validMaterialBinding(binding) || len(artifact.Bytes) == 0 || len(artifact.Bytes) > bootstrapHelperMaxBytes || !bootstrapDigestPattern.MatchString(artifact.SHA256) {
		return ""
	}
	hash := sha256.Sum256(artifact.Bytes)
	if hex.EncodeToString(hash[:]) != artifact.SHA256 {
		return ""
	}
	data, err := json.Marshal(struct {
		Version      string
		BindingID    string
		SessionID    string
		EpochID      string
		Generation   string
		VMID         int
		HelperSHA256 string
		Size         int
	}{"p28-bootstrap-helper-v1", binding.ID, binding.SessionID, binding.EpochID, binding.Generation, binding.VMID, artifact.SHA256, len(artifact.Bytes)})
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

// The helper is trusted Controller software, not a model-selected executable.
// Read a bounded immutable snapshot only after checking its explicit pin and
// Linux ownership/path protections. This function never executes the helper.
func verifyBootstrapHelperArtifact(ctx context.Context, file, pin string) (bootstrapHelperArtifact, error) {
	var empty bootstrapHelperArtifact
	if runtime.GOOS != "linux" || ctx == nil || ctx.Err() != nil || !filepath.IsAbs(file) || filepath.Clean(file) != file || !bootstrapDigestPattern.MatchString(pin) || noMaterialSymlinks(file) != nil {
		return empty, errBootstrapHelperArtifact
	}
	before, err := os.Lstat(file)
	if err != nil || !before.Mode().IsRegular() || !materialOwnerTrusted(before) || (before.Mode().Perm() != 0700 && before.Mode().Perm() != 0755) || before.Size() <= 0 || before.Size() > bootstrapHelperMaxBytes {
		return empty, errBootstrapHelperArtifact
	}
	f, err := os.Open(file)
	if err != nil {
		return empty, errBootstrapHelperArtifact
	}
	opened, err := f.Stat()
	if err != nil || !os.SameFile(before, opened) || opened.Size() != before.Size() || opened.Mode() != before.Mode() {
		_ = f.Close()
		return empty, errBootstrapHelperArtifact
	}
	data, readErr := io.ReadAll(io.LimitReader(bootstrapArtifactContextReader{ctx: ctx, reader: f}, bootstrapHelperMaxBytes+1))
	closeErr := f.Close()
	if readErr != nil || closeErr != nil || ctx.Err() != nil || int64(len(data)) != before.Size() {
		return empty, errBootstrapHelperArtifact
	}
	hash := sha256.Sum256(data)
	if hex.EncodeToString(hash[:]) != pin {
		return empty, errBootstrapHelperArtifact
	}
	after, err := os.Lstat(file)
	if err != nil || !os.SameFile(before, after) || after.Size() != before.Size() || after.Mode() != before.Mode() || !after.ModTime().Equal(before.ModTime()) || noMaterialSymlinks(file) != nil {
		return empty, errBootstrapHelperArtifact
	}
	return bootstrapHelperArtifact{Bytes: data, SHA256: pin}, nil
}
