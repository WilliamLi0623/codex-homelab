package sessionruntime

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/WilliamLi0623/codex-homelab/internal/store"
)

const (
	bootstrapCodex0155Version     = "0.155.0"
	bootstrapCodex0155Target      = "x86_64-unknown-linux-musl"
	bootstrapArtifactMaxBytes     = int64(512 << 20)
	bootstrapArtifactMaxFile      = int64(300 << 20)
	bootstrapArtifactManifestPath = ".codex-bootstrap-manifest.json"
)

var errBootstrapArtifact = errors.New("pinned Codex artifact bundle could not be verified")

type codexArtifactFile struct {
	path   string
	sha256 string
	mode   os.FileMode
}

type codexArtifactBundle struct {
	version string
	target  string
	files   []codexArtifactFile
}

// Hashes were read from the full working Codex 0.155.0 Linux bundle on
// LXC3006. This establishes reproducible homelab provenance, not an upstream
// vendor-signature claim. Keep the file set exact so tools/resources cannot
// be silently omitted while the main CLI hash still matches.
var bootstrapCodex0155Bundle = codexArtifactBundle{
	version: bootstrapCodex0155Version,
	target:  bootstrapCodex0155Target,
	files: []codexArtifactFile{
		{path: "bin/codex", sha256: "660e159a49e823ac8e5986cb238f73158ce4b957d40d9292f8de90862644b501", mode: 0755},
		{path: "bin/codex-code-mode-host", sha256: "32da418fe0481054d85909d0201636764c09ec147adb1d58297f2bfb78262096", mode: 0755},
		{path: "codex-package.json", sha256: "ddaf1668a475f4e203876d28c71d03bbb62fa4cec93197e4ab677c3514b6f2fd", mode: 0644},
		{path: "codex-path/rg", sha256: "e62198eb19b136b88c330af83647b5a962cb99b6b1f066758568f12de1974849", mode: 0755},
		{path: "codex-resources/bwrap", sha256: "7df960565a0dece99240ea4b9d0e011307817f9f3b73176c7b71fda44fe84765", mode: 0755},
		{path: "codex-resources/voice/NOTICE.md", sha256: "edadf0a401a293778fdb7416707b1ac55df26a686388d3b2db72cd2043763c16", mode: 0644},
		{path: "codex-resources/voice/bin/codex-voice-host", sha256: "b9ac679e9ca5ae1931eb19e9a27be13a73d0b94a6e2fca6a729263c099f572d6", mode: 0755},
		{path: "codex-resources/voice/lib/gstreamer-1.0/libgstapp.so", sha256: "fcaa396ce27d0fed13f52cddc663c6f00637cc79fbaedf1a9c7ad56403074158", mode: 0755},
		{path: "codex-resources/voice/lib/gstreamer-1.0/libgstaudioconvert.so", sha256: "825644aa0ec657e079ad2bdb7efd56a91c66e048cdd6e699cdb9dc9122b5ca81", mode: 0755},
		{path: "codex-resources/voice/lib/gstreamer-1.0/libgstaudioresample.so", sha256: "775afcd176410a4a0ffa2cbfc81b53c234da7023c3262d637441a8242a1eaaa7", mode: 0755},
		{path: "codex-resources/voice/lib/gstreamer-1.0/libgstcoreelements.so", sha256: "7719b06e9df09292d2e3ad7aa6ff55e719dc3a6b63bf5c0d5b761d63a2360352", mode: 0755},
		{path: "codex-resources/voice/lib/gstreamer-1.0/libgstopus.so", sha256: "d7c0f5794745f5256b99313f66368d14b2eb5c46c4a01a431d3bb35d61ed3982", mode: 0755},
		{path: "codex-resources/voice/lib/gstreamer-1.0/libgstrtp.so", sha256: "6f79865b27f2270296e6e3fb25224563f8241d32802caf0c723ec0351683a16b", mode: 0755},
		{path: "codex-resources/voice/lib/gstreamer-1.0/libgstrtpmanager.so", sha256: "9f435d97dd3828112ce0ef6e543b04be5e4cc27a7a30c80168d0b7c6e8dfc75b", mode: 0755},
		{path: "codex-resources/voice/lib/libffi.so.8", sha256: "6ef5db59b2e4b1eb63032fa6c8afb1669bc09b073789233c379efda85b1353db", mode: 0755},
		{path: "codex-resources/voice/lib/libgio-2.0.so.0", sha256: "6aab1019a4163e896af183f7453239f768b92fbcc8b2cd8a882d77e550ad4bbe", mode: 0755},
		{path: "codex-resources/voice/lib/libglib-2.0.so.0", sha256: "5220671c05c67e72fd685976d4e6289507f42b97387e184ce9d44823bef252ea", mode: 0755},
		{path: "codex-resources/voice/lib/libgmodule-2.0.so.0", sha256: "93bc2d03056e5263349af3201ae29cc5c2214a7bad058edfe37b2af0478b6903", mode: 0755},
		{path: "codex-resources/voice/lib/libgobject-2.0.so.0", sha256: "932c44dbe23dcb1d8d5ae6beb27f0f4e0dbc54f17dc98ac7df0c9765eaf9a60a", mode: 0755},
		{path: "codex-resources/voice/lib/libgstallocators-1.0.so.0", sha256: "1565d1ac104e8612017efd9c1b3b58a3d0ca61a4f098dc52d18d07df737cd060", mode: 0755},
		{path: "codex-resources/voice/lib/libgstapp-1.0.so.0", sha256: "21850e4861c2730885319c2cd2a9cfcf1299612236ab8f6c84e36f6f883287ff", mode: 0755},
		{path: "codex-resources/voice/lib/libgstaudio-1.0.so.0", sha256: "4399824113049cd210c159129bdcfc50064bd28ab046ddb91b018eb9e562748a", mode: 0755},
		{path: "codex-resources/voice/lib/libgstbase-1.0.so.0", sha256: "b6838e2a1e8bf885c7e153dbddaaa0c3d0ba46a2eec9d9f27ab7b782763f824d", mode: 0755},
		{path: "codex-resources/voice/lib/libgstnet-1.0.so.0", sha256: "e33424d4e299c115fc1ac2f2f475660a349e9c0d93d3f126c56d21654c78a650", mode: 0755},
		{path: "codex-resources/voice/lib/libgstpbutils-1.0.so.0", sha256: "ec8e8bad79230c3a3c98bc2b6c299cb22bdb1dc0caad053b7b443992ec749247", mode: 0755},
		{path: "codex-resources/voice/lib/libgstreamer-1.0.so.0", sha256: "114ae654cd595b455b6c8ba914ee0d8257b43cedb2033d08f86ac861642c1b95", mode: 0755},
		{path: "codex-resources/voice/lib/libgstrtp-1.0.so.0", sha256: "e1c1b60b8a9ade31de30f8ec9e8da7073a3d275ffa624ae105e2178fbc9adc28", mode: 0755},
		{path: "codex-resources/voice/lib/libgsttag-1.0.so.0", sha256: "b5bad18e079cf4e0f1bd9858221e65fe3e4be533aad6eade54c0e8acf6d4f5a2", mode: 0755},
		{path: "codex-resources/voice/lib/libgstvideo-1.0.so.0", sha256: "0773c07f4aa7ee2a20e801bef6858a922ccfacf022d24436422617e1264b96ff", mode: 0755},
		{path: "codex-resources/voice/lib/libintl.so.8", sha256: "f50f3ab34be7b3392f2db82bd67d50e133b35fa41d352dc3b3a97b70b0ffd960", mode: 0755},
		{path: "codex-resources/voice/lib/libopus.so.0", sha256: "79eb9be2127469d7d13db93814c1fd2162930cf47355d481f0783add2b559a9a", mode: 0755},
		{path: "codex-resources/voice/lib/libpcre2-8.so.0", sha256: "e39519b242f40686734ed8d0218f68cac24f0e956ec64c8c1b49b9da5cc9788b", mode: 0755},
		{path: "codex-resources/voice/lib/libz.so.1", sha256: "6c5d765f42d9368cc4bdbd11f725c1bcf6b14b78322033d3c94f787fc6261f5e", mode: 0755},
		{path: "codex-resources/voice/licenses/LGPL-2.1.txt", sha256: "ad2eec519ebd4b5df86ea84dff24ae3bfa2edea846a703b58902dd221ae375db", mode: 0644},
		{path: "codex-resources/voice/licenses/Opus.txt", sha256: "01e1167d54a096d123cf6dfbbeb19587278845c6481d2d66d545669846079551", mode: 0644},
		{path: "codex-resources/voice/licenses/PCRE2.md", sha256: "197d8a73ffee0d6b09adba2f9c677b5f5aede24edf89258a68e48248d010d811", mode: 0644},
		{path: "codex-resources/voice/licenses/libffi.txt", sha256: "17b64dc60f3b6897a60f971e288b973f655c2edcdf08b25f3c3dd5549857881c", mode: 0644},
		{path: "codex-resources/voice/licenses/proxy-libintl.txt", sha256: "d245807f90032872d1438d741ed21e2490e1175dc8aa3afa5ddb6c8e529b58e5", mode: 0644},
		{path: "codex-resources/voice/licenses/sljit.txt", sha256: "5f216505c0f6ea3273caec89e766eef93cdeb7bbb0c429f9360116d7c938feeb", mode: 0644},
		{path: "codex-resources/voice/licenses/zlib.txt", sha256: "e32ff4e00d9d94930537635291da39e7e612703334bf6fde8c7f1686fe8a45a2", mode: 0644},
		{path: "codex-resources/voice/manifest.json", sha256: "485dd1aec98eccdc528add01e6d202d6e63cd44b80505032d4205c4d6b32b84d", mode: 0644},
		{path: "codex-resources/voice/runtime.json", sha256: "c3612eb11768ef2c6116126b1af85bc15ff549b6a9fb335635ab861cbbaeba7c", mode: 0755},
		{path: "codex-resources/voice/sources.json", sha256: "e2f6b124f6277e7c3c4f5c7cc8830c66190f671ad65e972bb2cc9507dbfcb937", mode: 0644},
		{path: "codex-resources/zsh/bin/zsh", sha256: "67faaaa89242c4a332e16e508a1977cffc24bf7fca31d4411cdfd101f3831ef3", mode: 0755},
	},
}

type verifiedCodexArtifact struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	Mode   uint32 `json:"mode"`
}

func verifyBootstrapArtifactBundle(ctx context.Context, root string, bundle codexArtifactBundle) (store.SessionBootstrapEvidence, error) {
	if runtime.GOOS != "linux" || ctx == nil || ctx.Err() != nil || !filepath.IsAbs(root) || filepath.Clean(root) != root || bundle.version == "" || bundle.target != bootstrapCodex0155Target || len(bundle.files) == 0 || len(bundle.files) > 128 || noMaterialSymlinks(root) != nil {
		return store.SessionBootstrapEvidence{}, errBootstrapArtifact
	}
	rootInfo, err := os.Lstat(root)
	if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 || rootInfo.Mode().Perm()&0022 != 0 || !materialOwnerTrusted(rootInfo) {
		return store.SessionBootstrapEvidence{}, errBootstrapArtifact
	}
	expected := make(map[string]codexArtifactFile, len(bundle.files))
	expectedDirs := map[string]struct{}{".": {}}
	for _, file := range bundle.files {
		clean := filepath.Clean(filepath.FromSlash(file.path))
		if file.path == "" || strings.ContainsAny(file.path, "\\\x00\r\n") || strings.HasPrefix(file.path, "/") || clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || filepath.ToSlash(clean) != file.path || !bootstrapDigestPattern.MatchString(file.sha256) || (file.mode != 0644 && file.mode != 0755) {
			return store.SessionBootstrapEvidence{}, errBootstrapArtifact
		}
		if _, duplicate := expected[file.path]; duplicate {
			return store.SessionBootstrapEvidence{}, errBootstrapArtifact
		}
		expected[file.path] = file
		for parent := filepath.Dir(clean); parent != "."; parent = filepath.Dir(parent) {
			expectedDirs[filepath.ToSlash(parent)] = struct{}{}
		}
	}
	seen := make(map[string]os.FileInfo, len(expected))
	walkErr := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || ctx.Err() != nil {
			return errBootstrapArtifact
		}
		if path == root {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return errBootstrapArtifact
		}
		rel = filepath.ToSlash(rel)
		info, err := entry.Info()
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !materialOwnerTrusted(info) || info.Mode().Perm()&0022 != 0 {
			return errBootstrapArtifact
		}
		if entry.IsDir() {
			if _, allowed := expectedDirs[rel]; !allowed {
				return errBootstrapArtifact
			}
			return nil
		}
		if !info.Mode().IsRegular() {
			return errBootstrapArtifact
		}
		if _, allowed := expected[rel]; !allowed {
			return errBootstrapArtifact
		}
		seen[rel] = info
		return nil
	})
	if walkErr != nil || ctx.Err() != nil || len(seen) != len(expected) {
		return store.SessionBootstrapEvidence{}, errBootstrapArtifact
	}

	entries := make([]verifiedCodexArtifact, 0, len(expected))
	var total int64
	for rel, file := range expected {
		before := seen[rel]
		if before == nil || before.Size() <= 0 || before.Size() > bootstrapArtifactMaxFile || before.Mode().Perm() != file.mode {
			return store.SessionBootstrapEvidence{}, errBootstrapArtifact
		}
		handle, err := os.Open(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			return store.SessionBootstrapEvidence{}, errBootstrapArtifact
		}
		opened, statErr := handle.Stat()
		if statErr != nil || !os.SameFile(before, opened) || opened.Size() != before.Size() {
			_ = handle.Close()
			return store.SessionBootstrapEvidence{}, errBootstrapArtifact
		}
		hash := sha256.New()
		n, copyErr := io.Copy(hash, io.LimitReader(bootstrapArtifactContextReader{ctx: ctx, reader: handle}, bootstrapArtifactMaxFile+1))
		closeErr := handle.Close()
		if copyErr != nil || closeErr != nil || n != before.Size() || hex.EncodeToString(hash.Sum(nil)) != file.sha256 {
			return store.SessionBootstrapEvidence{}, errBootstrapArtifact
		}
		after, err := os.Lstat(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil || after.Mode()&os.ModeSymlink != 0 || !os.SameFile(before, after) || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
			return store.SessionBootstrapEvidence{}, errBootstrapArtifact
		}
		total += n
		if total > bootstrapArtifactMaxBytes {
			return store.SessionBootstrapEvidence{}, errBootstrapArtifact
		}
		entries = append(entries, verifiedCodexArtifact{Path: rel, SHA256: file.sha256, Size: n, Mode: uint32(file.mode.Perm())})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	data, err := json.Marshal(struct {
		Version string                  `json:"version"`
		Target  string                  `json:"target"`
		Files   []verifiedCodexArtifact `json:"files"`
	}{bundle.version, bundle.target, entries})
	if err != nil || ctx.Err() != nil {
		return store.SessionBootstrapEvidence{}, errBootstrapArtifact
	}
	digest := sha256.Sum256(data)
	return store.SessionBootstrapEvidence{SHA256: hex.EncodeToString(digest[:])}, nil
}

type bootstrapArtifactContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r bootstrapArtifactContextReader) Read(data []byte) (int, error) {
	if r.ctx == nil || r.ctx.Err() != nil {
		return 0, errBootstrapArtifact
	}
	return r.reader.Read(data)
}

// writeBootstrapArtifactTarFiles emits a deterministic archive from a bundle
// whose manifest was verified immediately before this call. It rechecks every
// file while copying so a changed path, mode, size, or content aborts the
// stream. The caller must not treat an error as permission to retry an upload.
func writeBootstrapArtifactTarFiles(ctx context.Context, root string, bundle codexArtifactBundle, verified []verifiedCodexArtifact, output io.Writer) error {
	if ctx == nil || ctx.Err() != nil || output == nil || !filepath.IsAbs(root) || filepath.Clean(root) != root || bundle.version == "" || bundle.target != bootstrapCodex0155Target || len(bundle.files) == 0 || len(bundle.files) != len(verified) {
		return errBootstrapArtifact
	}
	entries := make(map[string]verifiedCodexArtifact, len(verified))
	for _, entry := range verified {
		if entry.Path == "" || entry.Path != filepath.ToSlash(filepath.Clean(filepath.FromSlash(entry.Path))) || entry.Size <= 0 || entry.Size > bootstrapArtifactMaxFile || !bootstrapDigestPattern.MatchString(entry.SHA256) || (entry.Mode != uint32(os.FileMode(0644).Perm()) && entry.Mode != uint32(os.FileMode(0755).Perm())) {
			return errBootstrapArtifact
		}
		if _, duplicate := entries[entry.Path]; duplicate {
			return errBootstrapArtifact
		}
		entries[entry.Path] = entry
	}
	files := append([]codexArtifactFile(nil), bundle.files...)
	sort.Slice(files, func(i, j int) bool { return files[i].path < files[j].path })
	var total int64
	seen := make(map[string]struct{}, len(files))
	for _, file := range files {
		cleanPath := filepath.ToSlash(filepath.Clean(filepath.FromSlash(file.path)))
		if file.path == "" || file.path == "." || file.path == ".." || strings.HasPrefix(file.path, "../") || strings.ContainsAny(file.path, "\\\x00\r\n") || strings.HasPrefix(file.path, "/") || cleanPath != file.path || !bootstrapDigestPattern.MatchString(file.sha256) || (file.mode != 0644 && file.mode != 0755) {
			return errBootstrapArtifact
		}
		if _, duplicate := seen[file.path]; duplicate {
			return errBootstrapArtifact
		}
		seen[file.path] = struct{}{}
		entry, ok := entries[file.path]
		if !ok || entry.SHA256 != file.sha256 || entry.Mode != uint32(file.mode.Perm()) {
			return errBootstrapArtifact
		}
		total += entry.Size
		if total > bootstrapArtifactMaxBytes {
			return errBootstrapArtifact
		}
	}
	if len(seen) != len(entries) {
		return errBootstrapArtifact
	}
	manifestFiles := make([]verifiedCodexArtifact, 0, len(files))
	for _, file := range files {
		manifestFiles = append(manifestFiles, entries[file.path])
	}
	manifestData, err := json.Marshal(makeBootstrapCodexManifest(bundle, manifestFiles))
	if err != nil || int64(len(manifestData)) > 1<<20 {
		return errBootstrapArtifact
	}

	writer := tar.NewWriter(output)
	if err := writer.WriteHeader(&tar.Header{Name: bootstrapArtifactManifestPath, Mode: 0600, Size: int64(len(manifestData)), Uid: 0, Gid: 0, ModTime: time.Unix(0, 0).UTC(), Typeflag: tar.TypeReg, Format: tar.FormatPAX}); err != nil {
		return errBootstrapArtifact
	}
	if n, err := writer.Write(manifestData); err != nil || n != len(manifestData) {
		return errBootstrapArtifact
	}
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return errBootstrapArtifact
		}
		entry := entries[file.path]
		fullPath := filepath.Join(root, filepath.FromSlash(file.path))
		before, err := os.Lstat(fullPath)
		if err != nil || before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() || (runtime.GOOS != "windows" && before.Mode().Perm() != file.mode) || before.Size() != entry.Size {
			return errBootstrapArtifact
		}
		handle, err := os.Open(fullPath)
		if err != nil {
			return errBootstrapArtifact
		}
		opened, statErr := handle.Stat()
		if statErr != nil || !os.SameFile(before, opened) || opened.Size() != entry.Size || (runtime.GOOS != "windows" && opened.Mode().Perm() != file.mode) {
			_ = handle.Close()
			return errBootstrapArtifact
		}
		header := &tar.Header{
			Name: file.path, Mode: int64(file.mode.Perm()), Size: entry.Size,
			Uid: 0, Gid: 0, ModTime: time.Unix(0, 0).UTC(), Typeflag: tar.TypeReg,
			Format: tar.FormatPAX,
		}
		if err := writer.WriteHeader(header); err != nil {
			_ = handle.Close()
			return errBootstrapArtifact
		}
		hash := sha256.New()
		copied, copyErr := io.CopyN(io.MultiWriter(writer, hash), bootstrapArtifactContextReader{ctx: ctx, reader: handle}, entry.Size)
		var extra [1]byte
		extraN, extraErr := handle.Read(extra[:])
		after, afterErr := os.Lstat(fullPath)
		closeErr := handle.Close()
		if copyErr != nil || copied != entry.Size || extraN != 0 || (extraErr != nil && extraErr != io.EOF) || afterErr != nil || !os.SameFile(before, after) || after.Size() != entry.Size || (runtime.GOOS != "windows" && after.Mode().Perm() != file.mode) || !after.ModTime().Equal(before.ModTime()) || closeErr != nil || hex.EncodeToString(hash.Sum(nil)) != file.sha256 {
			return errBootstrapArtifact
		}
	}
	if err := ctx.Err(); err != nil {
		return errBootstrapArtifact
	}
	if err := writer.Close(); err != nil {
		return errBootstrapArtifact
	}
	return nil
}
