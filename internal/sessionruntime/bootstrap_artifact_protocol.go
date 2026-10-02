package sessionruntime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"hash"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/WilliamLi0623/codex-homelab/internal/store"
)

const (
	bootstrapArtifactTransferMagic = "P28CODXARTIFACT1\n"
	bootstrapArtifactCommitMagic   = "P28CODXCOMMIT1\n"
	bootstrapArtifactFrameHeader   = len(bootstrapArtifactTransferMagic) + 8 + sha256.Size
)

// writeBootstrapArtifactTransfer writes a two-pass framed protocol. The
// receiver cannot promote an installation until it receives the final digest
// and commit marker, which the sender emits only after both source passes and
// a final exact-bundle verification succeed.
func writeBootstrapArtifactTransfer(ctx context.Context, root string, bundle codexArtifactBundle, expected store.SessionBootstrapEvidence, output io.Writer) error {
	if ctx == nil || output == nil || !validBootstrapEvidence(expected) {
		return errBootstrapArtifact
	}

	verified, err := verifyBootstrapArtifactBundle(ctx, root, bundle)
	if err != nil || verified != expected {
		return errBootstrapArtifact
	}

	firstPass := newBootstrapArtifactCountingHashWriter(io.Discard)
	entries, err := bootstrapArtifactEntries(root, bundle)
	if err != nil {
		return errBootstrapArtifact
	}
	if err := writeBootstrapArtifactTarFiles(ctx, root, bundle, entries, firstPass); err != nil || firstPass.count <= 0 || firstPass.count > bootstrapArtifactMaxBytes {
		return errBootstrapArtifact
	}
	verified, err = verifyBootstrapArtifactBundle(ctx, root, bundle)
	if err != nil || verified != expected {
		return errBootstrapArtifact
	}

	var header [bootstrapArtifactFrameHeader]byte
	copy(header[:], bootstrapArtifactTransferMagic)
	binary.BigEndian.PutUint64(header[len(bootstrapArtifactTransferMagic):], uint64(firstPass.count))
	copy(header[len(bootstrapArtifactTransferMagic)+8:], firstPass.hash.Sum(nil))
	if err := writeBootstrapArtifactAll(output, header[:]); err != nil {
		return errBootstrapArtifact
	}

	secondPass := newBootstrapArtifactCountingHashWriter(output)
	if err := writeBootstrapArtifactTarFiles(ctx, root, bundle, entries, secondPass); err != nil || secondPass.count != firstPass.count || !bytes.Equal(secondPass.hash.Sum(nil), firstPass.hash.Sum(nil)) {
		return errBootstrapArtifact
	}
	verified, err = verifyBootstrapArtifactBundle(ctx, root, bundle)
	if err != nil || verified != expected {
		return errBootstrapArtifact
	}
	if err := writeBootstrapArtifactAll(output, firstPass.hash.Sum(nil)); err != nil {
		return errBootstrapArtifact
	}
	if err := writeBootstrapArtifactAll(output, []byte(bootstrapArtifactCommitMagic)); err != nil {
		return errBootstrapArtifact
	}
	return nil
}

func bootstrapArtifactEntries(root string, bundle codexArtifactBundle) ([]verifiedCodexArtifact, error) {
	entries := make([]verifiedCodexArtifact, 0, len(bundle.files))
	for _, file := range bundle.files {
		if file.path == "" || strings.ContainsAny(file.path, "\\\x00\r\n") || strings.HasPrefix(file.path, "/") || filepath.ToSlash(filepath.Clean(filepath.FromSlash(file.path))) != file.path || file.path == ".." || strings.HasPrefix(file.path, "../") {
			return nil, errBootstrapArtifact
		}
		info, err := os.Lstat(filepath.Join(root, filepath.FromSlash(file.path)))
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() <= 0 || info.Size() > bootstrapArtifactMaxFile {
			return nil, errBootstrapArtifact
		}
		entries = append(entries, verifiedCodexArtifact{Path: file.path, SHA256: file.sha256, Size: info.Size(), Mode: uint32(file.mode.Perm())})
	}
	return entries, nil
}

type bootstrapArtifactCountingHashWriter struct {
	output io.Writer
	hash   hash.Hash
	count  int64
}

func newBootstrapArtifactCountingHashWriter(output io.Writer) *bootstrapArtifactCountingHashWriter {
	return &bootstrapArtifactCountingHashWriter{output: output, hash: sha256.New()}
}

func (w *bootstrapArtifactCountingHashWriter) Write(data []byte) (int, error) {
	if w == nil || w.output == nil || w.hash == nil || int64(len(data)) > bootstrapArtifactMaxBytes-w.count {
		return 0, errBootstrapArtifact
	}
	n, err := w.output.Write(data)
	if n > 0 {
		_, _ = w.hash.Write(data[:n])
		w.count += int64(n)
	}
	if err != nil {
		return n, err
	}
	if n != len(data) {
		return n, io.ErrShortWrite
	}
	return n, nil
}

func writeBootstrapArtifactAll(output io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := output.Write(data)
		if n < 0 || n > len(data) || err != nil || n == 0 {
			return errBootstrapArtifact
		}
		data = data[n:]
	}
	return nil
}
