package sessionruntime

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/WilliamLi0623/codex-homelab/internal/store"
)

var errBootstrapCodexInstall = errors.New("Codex artifact installation could not be verified")

// InstallBootstrapCodexArtifact is the fixed guest-side receiver entrypoint.
// The caller supplies only the validated generation-bound install digest; the
// installer accepts one framed archive and never repairs or replaces paths.
func InstallBootstrapCodexArtifact(ctx context.Context, input io.Reader, generationDigest string) (store.SessionBootstrapEvidence, error) {
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		return store.SessionBootstrapEvidence{}, errBootstrapCodexInstall
	}
	return installBootstrapCodexBundleAt(ctx, input, generationDigest, bootstrapCodex0155Bundle, "/opt/codex", "/usr/local/bin/codex")
}

// ObserveBootstrapCodexArtifact verifies the generation install and launcher
// without creating, replacing, or deleting guest paths.
func ObserveBootstrapCodexArtifact(ctx context.Context, generationDigest string) (store.SessionBootstrapEvidence, error) {
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		return store.SessionBootstrapEvidence{}, errBootstrapCodexInstall
	}
	evidence, verified, err := observeBootstrapCodexBundleAt(ctx, generationDigest, bootstrapCodex0155Bundle, "/opt/codex", "/usr/local/bin/codex")
	if err != nil || !verified {
		return store.SessionBootstrapEvidence{}, errBootstrapCodexInstall
	}
	return evidence, nil
}

func observeBootstrapCodexBundleAt(ctx context.Context, generationDigest string, bundle codexArtifactBundle, installRoot, launcher string) (store.SessionBootstrapEvidence, bool, error) {
	if ctx == nil || ctx.Err() != nil || !bootstrapDigestPattern.MatchString(generationDigest) || !validInstallBundle(bundle) || !filepath.IsAbs(installRoot) || filepath.Clean(installRoot) != installRoot || !filepath.IsAbs(launcher) || filepath.Clean(launcher) != launcher {
		return store.SessionBootstrapEvidence{}, false, errBootstrapCodexInstall
	}
	if validateArtifactInstallDirectory(installRoot, false) != nil || validateArtifactInstallDirectory(filepath.Dir(launcher), false) != nil {
		return store.SessionBootstrapEvidence{}, false, errBootstrapCodexInstall
	}
	final := filepath.Join(installRoot, generationDigest)
	if validateArtifactInstallDirectory(final, false) != nil {
		return store.SessionBootstrapEvidence{}, false, errBootstrapCodexInstall
	}
	linkInfo, err := os.Lstat(launcher)
	if err != nil || linkInfo.Mode()&os.ModeSymlink == 0 || !materialOwnerTrusted(linkInfo) {
		return store.SessionBootstrapEvidence{}, false, errBootstrapCodexInstall
	}
	link, err := os.Readlink(launcher)
	if err != nil || link != filepath.Join(final, "bin", "codex") {
		return store.SessionBootstrapEvidence{}, false, errBootstrapCodexInstall
	}
	if _, err := verifyBootstrapArtifactBundle(ctx, final, bundle); err != nil {
		return store.SessionBootstrapEvidence{}, false, errBootstrapCodexInstall
	}
	entries, err := bootstrapArtifactEntries(final, bundle)
	if err != nil {
		return store.SessionBootstrapEvidence{}, false, errBootstrapCodexInstall
	}
	manifest := makeBootstrapCodexManifest(bundle, entries)
	return bootstrapCodexInstallEvidence(generationDigest, bundle, manifest), true, nil
}

func installBootstrapCodexBundleAt(ctx context.Context, input io.Reader, generationDigest string, bundle codexArtifactBundle, installRoot, launcher string) (store.SessionBootstrapEvidence, error) {
	if ctx == nil || ctx.Err() != nil || input == nil || runtime.GOOS != "linux" || os.Geteuid() != 0 || !bootstrapDigestPattern.MatchString(generationDigest) || !validInstallBundle(bundle) || !filepath.IsAbs(installRoot) || filepath.Clean(installRoot) != installRoot || !filepath.IsAbs(launcher) || filepath.Clean(launcher) != launcher {
		return store.SessionBootstrapEvidence{}, errBootstrapCodexInstall
	}
	if err := validateArtifactInstallDirectory(installRoot, true); err != nil {
		return store.SessionBootstrapEvidence{}, errBootstrapCodexInstall
	}
	if err := validateArtifactInstallDirectory(filepath.Dir(launcher), false); err != nil {
		return store.SessionBootstrapEvidence{}, errBootstrapCodexInstall
	}
	final := filepath.Join(installRoot, generationDigest)
	staging := filepath.Join(installRoot, ".staging-"+generationDigest)
	if pathExists(final) || pathExists(staging) || pathExists(launcher) {
		return store.SessionBootstrapEvidence{}, errBootstrapCodexInstall
	}
	if err := os.Mkdir(staging, 0700); err != nil {
		return store.SessionBootstrapEvidence{}, errBootstrapCodexInstall
	}
	if err := syncArtifactDirectory(installRoot); err != nil {
		return store.SessionBootstrapEvidence{}, errBootstrapCodexInstall
	}

	var frameHeader [bootstrapArtifactFrameHeader]byte
	if _, err := io.ReadFull(input, frameHeader[:]); err != nil || string(frameHeader[:len(bootstrapArtifactTransferMagic)]) != bootstrapArtifactTransferMagic {
		return store.SessionBootstrapEvidence{}, errBootstrapCodexInstall
	}
	lengthOffset := len(bootstrapArtifactTransferMagic)
	archiveLength := int64(binary.BigEndian.Uint64(frameHeader[lengthOffset : lengthOffset+8]))
	if archiveLength <= 0 || archiveLength > bootstrapArtifactMaxBytes {
		return store.SessionBootstrapEvidence{}, errBootstrapCodexInstall
	}
	expectedArchiveSHA := append([]byte(nil), frameHeader[lengthOffset+8:]...)
	bounded := &io.LimitedReader{R: input, N: archiveLength}
	archiveHash := sha256.New()
	archiveBody := io.TeeReader(bounded, archiveHash)
	archive := tar.NewReader(archiveBody)
	manifest, err := readBootstrapCodexManifest(archive, bundle)
	if err != nil {
		return store.SessionBootstrapEvidence{}, errBootstrapCodexInstall
	}
	if err := extractBootstrapCodexFiles(ctx, archive, staging, manifest); err != nil {
		return store.SessionBootstrapEvidence{}, errBootstrapCodexInstall
	}
	if err := drainBootstrapArtifactPadding(archiveBody); err != nil || bounded.N != 0 || !bytes.Equal(archiveHash.Sum(nil), expectedArchiveSHA) {
		return store.SessionBootstrapEvidence{}, errBootstrapCodexInstall
	}
	var commitDigest [sha256.Size]byte
	if _, err := io.ReadFull(input, commitDigest[:]); err != nil || !bytes.Equal(commitDigest[:], expectedArchiveSHA) {
		return store.SessionBootstrapEvidence{}, errBootstrapCodexInstall
	}
	var commitMarker [len(bootstrapArtifactCommitMagic)]byte
	if _, err := io.ReadFull(input, commitMarker[:]); err != nil || string(commitMarker[:]) != bootstrapArtifactCommitMagic {
		return store.SessionBootstrapEvidence{}, errBootstrapCodexInstall
	}
	var trailing [1]byte
	if n, err := input.Read(trailing[:]); n != 0 || err != io.EOF {
		return store.SessionBootstrapEvidence{}, errBootstrapCodexInstall
	}
	if err := ctx.Err(); err != nil {
		return store.SessionBootstrapEvidence{}, errBootstrapCodexInstall
	}
	if err := syncArtifactTree(staging); err != nil {
		return store.SessionBootstrapEvidence{}, errBootstrapCodexInstall
	}
	if err := validateInstalledCodexExecutable(filepath.Join(staging, "bin", "codex"), bundle); err != nil {
		return store.SessionBootstrapEvidence{}, errBootstrapCodexInstall
	}
	if err := renameArtifactDirectoryNoReplace(staging, final); err != nil {
		return store.SessionBootstrapEvidence{}, errBootstrapCodexInstall
	}
	if err := syncArtifactDirectory(installRoot); err != nil {
		return store.SessionBootstrapEvidence{}, errBootstrapCodexInstall
	}
	executable := filepath.Join(final, "bin", "codex")
	if err := validateInstalledCodexExecutable(executable, bundle); err != nil {
		return store.SessionBootstrapEvidence{}, errBootstrapCodexInstall
	}
	if err := os.Symlink(executable, launcher); err != nil {
		return store.SessionBootstrapEvidence{}, errBootstrapCodexInstall
	}
	if err := syncArtifactDirectory(filepath.Dir(launcher)); err != nil {
		return store.SessionBootstrapEvidence{}, errBootstrapCodexInstall
	}
	return bootstrapCodexInstallEvidence(generationDigest, bundle, manifest), nil
}

type bootstrapCodexManifest struct {
	Version string                  `json:"version"`
	Target  string                  `json:"target"`
	Files   []verifiedCodexArtifact `json:"files"`
}

func makeBootstrapCodexManifest(bundle codexArtifactBundle, entries []verifiedCodexArtifact) bootstrapCodexManifest {
	files := append([]verifiedCodexArtifact(nil), entries...)
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return bootstrapCodexManifest{Version: bundle.version, Target: bundle.target, Files: files}
}

func readBootstrapCodexManifest(archive *tar.Reader, bundle codexArtifactBundle) (bootstrapCodexManifest, error) {
	var empty bootstrapCodexManifest
	header, err := archive.Next()
	if err != nil || header.Name != bootstrapArtifactManifestPath || header.Typeflag != tar.TypeReg || header.Mode != 0600 || header.Size <= 0 || header.Size > 1<<20 || header.Uid != 0 || header.Gid != 0 || header.Linkname != "" {
		return empty, errBootstrapCodexInstall
	}
	data, err := io.ReadAll(io.LimitReader(archive, (1<<20)+1))
	if err != nil || int64(len(data)) != header.Size {
		return empty, errBootstrapCodexInstall
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var manifest bootstrapCodexManifest
	if decoder.Decode(&manifest) != nil {
		return empty, errBootstrapCodexInstall
	}
	var trailing any
	canonical, marshalErr := json.Marshal(manifest)
	if decoder.Decode(&trailing) != io.EOF || marshalErr != nil || !bytes.Equal(canonical, data) || manifest.Version != bundle.version || manifest.Target != bundle.target || len(manifest.Files) != len(bundle.files) {
		return empty, errBootstrapCodexInstall
	}
	expected := append([]codexArtifactFile(nil), bundle.files...)
	sort.Slice(expected, func(i, j int) bool { return expected[i].path < expected[j].path })
	var total int64
	for i, file := range expected {
		entry := manifest.Files[i]
		if entry.Path != file.path || entry.SHA256 != file.sha256 || entry.Mode != uint32(file.mode.Perm()) || entry.Size <= 0 || entry.Size > bootstrapArtifactMaxFile {
			return empty, errBootstrapCodexInstall
		}
		total += entry.Size
		if total > bootstrapArtifactMaxBytes {
			return empty, errBootstrapCodexInstall
		}
	}
	return manifest, nil
}

func extractBootstrapCodexFiles(ctx context.Context, archive *tar.Reader, staging string, manifest bootstrapCodexManifest) error {
	expected := make(map[string]verifiedCodexArtifact, len(manifest.Files))
	for _, entry := range manifest.Files {
		expected[entry.Path] = entry
	}
	seen := make(map[string]struct{}, len(expected))
	for {
		if err := ctx.Err(); err != nil {
			return errBootstrapCodexInstall
		}
		header, err := archive.Next()
		if err == io.EOF {
			break
		}
		if err != nil || header.Typeflag != tar.TypeReg || header.Linkname != "" || header.Uid != 0 || header.Gid != 0 || header.Size <= 0 {
			return errBootstrapCodexInstall
		}
		entry, ok := expected[header.Name]
		if !ok || header.Mode != int64(entry.Mode) || header.Size != entry.Size {
			return errBootstrapCodexInstall
		}
		if _, duplicate := seen[header.Name]; duplicate {
			return errBootstrapCodexInstall
		}
		seen[header.Name] = struct{}{}
		name, valid := cleanBootstrapArchiveName(header.Name, bootstrapArchiveMaxPathBytes)
		if !valid || name != header.Name {
			return errBootstrapCodexInstall
		}
		destination := filepath.Join(staging, filepath.FromSlash(name))
		if !strings.HasPrefix(destination, staging+string(filepath.Separator)) {
			return errBootstrapCodexInstall
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0755); err != nil {
			return errBootstrapCodexInstall
		}
		file, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return errBootstrapCodexInstall
		}
		hash := sha256.New()
		written, copyErr := io.CopyN(io.MultiWriter(file, hash), bootstrapArtifactContextReader{ctx: ctx, reader: archive}, entry.Size)
		chmodErr := file.Chmod(os.FileMode(entry.Mode))
		syncErr := file.Sync()
		closeErr := file.Close()
		if copyErr != nil || written != entry.Size || syncErr != nil || chmodErr != nil || closeErr != nil || hex.EncodeToString(hash.Sum(nil)) != entry.SHA256 {
			return errBootstrapCodexInstall
		}
	}
	if len(seen) != len(expected) {
		return errBootstrapCodexInstall
	}
	return nil
}

func drainBootstrapArtifactPadding(input io.Reader) error {
	var buffer [32 * 1024]byte
	for {
		n, err := input.Read(buffer[:])
		for _, value := range buffer[:n] {
			if value != 0 {
				return errBootstrapCodexInstall
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil || n == 0 {
			return errBootstrapCodexInstall
		}
	}
}

func validInstallBundle(bundle codexArtifactBundle) bool {
	if bundle.version == "" || bundle.target != bootstrapCodex0155Target || len(bundle.files) == 0 || len(bundle.files) > 128 {
		return false
	}
	seen := make(map[string]struct{}, len(bundle.files))
	for _, file := range bundle.files {
		if file.path == "" || strings.ContainsAny(file.path, "\\\x00\r\n") || strings.HasPrefix(file.path, "/") || file.path != filepath.ToSlash(filepath.Clean(filepath.FromSlash(file.path))) || file.path == ".." || strings.HasPrefix(file.path, "../") || !bootstrapDigestPattern.MatchString(file.sha256) || (file.mode != 0644 && file.mode != 0755) {
			return false
		}
		if _, ok := seen[file.path]; ok {
			return false
		}
		seen[file.path] = struct{}{}
	}
	_, hasCLI := seen["bin/codex"]
	for _, file := range bundle.files {
		if file.path == "bin/codex" {
			return hasCLI && file.mode == 0755
		}
	}
	return false
}

func validateArtifactInstallDirectory(path string, allowMissing bool) error {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errBootstrapCodexInstall
	}
	var missing []string
	for current := path; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) && allowMissing {
			missing = append(missing, current)
			if filepath.Dir(current) == current {
				break
			}
			continue
		}
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || !materialOwnerTrusted(info) || (info.Mode().Perm()&0022 != 0 && !(info.Mode()&os.ModeSticky != 0 && info.Mode().Perm()&0002 != 0)) {
			return errBootstrapCodexInstall
		}
		if filepath.Dir(current) == current {
			break
		}
	}
	for i := len(missing) - 1; i >= 0; i-- {
		if err := os.Mkdir(missing[i], 0755); err != nil {
			return errBootstrapCodexInstall
		}
		if err := syncArtifactDirectory(filepath.Dir(missing[i])); err != nil {
			return errBootstrapCodexInstall
		}
	}
	return nil
}

func pathExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil || !errors.Is(err, os.ErrNotExist)
}

func validateInstalledCodexExecutable(path string, bundle codexArtifactBundle) error {
	var expected *codexArtifactFile
	for i := range bundle.files {
		if bundle.files[i].path == "bin/codex" {
			expected = &bundle.files[i]
			break
		}
	}
	info, err := os.Lstat(path)
	if expected == nil || err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0755 || info.Size() <= 0 || info.Size() > bootstrapArtifactMaxFile || !materialOwnerTrusted(info) {
		return errBootstrapCodexInstall
	}
	handle, err := os.Open(path)
	if err != nil {
		return errBootstrapCodexInstall
	}
	hash := sha256.New()
	count, copyErr := io.Copy(hash, io.LimitReader(handle, bootstrapArtifactMaxFile+1))
	closeErr := handle.Close()
	if copyErr != nil || count != info.Size() || closeErr != nil || hex.EncodeToString(hash.Sum(nil)) != expected.sha256 {
		return errBootstrapCodexInstall
	}
	return nil
}

func syncArtifactDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return errBootstrapCodexInstall
	}
	err = directory.Sync()
	closeErr := directory.Close()
	if err != nil || closeErr != nil {
		return errBootstrapCodexInstall
	}
	return nil
}

func syncArtifactTree(root string) error {
	var directories []string
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.Type()&os.ModeSymlink != 0 {
			return errBootstrapCodexInstall
		}
		if entry.IsDir() {
			directories = append(directories, path)
		}
		return nil
	}); err != nil {
		return errBootstrapCodexInstall
	}
	for i := len(directories) - 1; i >= 0; i-- {
		if err := syncArtifactDirectory(directories[i]); err != nil {
			return errBootstrapCodexInstall
		}
	}
	return nil
}

func bootstrapCodexInstallEvidence(digest string, bundle codexArtifactBundle, manifest bootstrapCodexManifest) store.SessionBootstrapEvidence {
	data, _ := json.Marshal(struct {
		GenerationArtifactDigest string `json:"generation_artifact_digest"`
		Version                  string `json:"version"`
		Target                   string `json:"target"`
		ManifestSHA256           string `json:"manifest_sha256"`
	}{digest, bundle.version, bundle.target, bootstrapCodexManifestDigest(manifest)})
	sum := sha256.Sum256(data)
	return store.SessionBootstrapEvidence{SHA256: hex.EncodeToString(sum[:])}
}

func bootstrapCodexManifestDigest(manifest bootstrapCodexManifest) string {
	data, _ := json.Marshal(manifest)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func bootstrapCodexGenerationArtifactDigest(binding store.SessionRuntimeBinding, bundle codexArtifactBundle, evidence store.SessionBootstrapEvidence) string {
	if !validMaterialBinding(binding) || !validInstallBundle(bundle) || !validBootstrapEvidence(evidence) {
		return ""
	}
	identity := struct {
		Binding        store.SessionRuntimeBinding `json:"binding"`
		Version        string                      `json:"version"`
		Target         string                      `json:"target"`
		ArtifactSHA256 string                      `json:"artifact_sha256"`
	}{materialBinding(binding), bundle.version, bundle.target, evidence.SHA256}
	data, err := json.Marshal(identity)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
