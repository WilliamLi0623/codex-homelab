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

var errBootstrapBackup = errors.New("private bootstrap backup could not be verified; do not replay")

type bootstrapBackupStore struct{ root string }
type bootstrapBackupManifest struct {
	Version int
	Policy  string
	Binding store.SessionRuntimeBinding
	Export  bootstrapTLSArchiveEvidence
}

func newBootstrapBackupStore(root string) (*bootstrapBackupStore, error) {
	if runtime.GOOS != "linux" || !filepath.IsAbs(root) || filepath.Clean(root) != root || root == string(filepath.Separator) || privateMaterialDirectory(root) != nil {
		return nil, errBootstrapBackup
	}
	return &bootstrapBackupStore{root: root}, nil
}

func (s *bootstrapBackupStore) directory(binding store.SessionRuntimeBinding) string {
	data, _ := json.Marshal(materialBinding(binding))
	// Keep the original on-disk namespace so prior TLS-only evidence is not
	// orphaned when the manifest adds additional exact archive policies.
	return filepath.Join(s.root, "tls-"+materialDigest(data))
}

// save is exclusive, never repairs a partial directory and never removes data.
// The production caller must hold a fresh durable ImageBackupCreated-stage claim
// before invoking it. Completion proves a private durable archive only,
// not permission to delete guest files or a completed sanitation stage.
func (s *bootstrapBackupStore) save(ctx context.Context, binding store.SessionRuntimeBinding, export func(context.Context, store.SessionRuntimeBinding, io.Writer) (bootstrapTLSArchiveEvidence, error)) (store.SessionBootstrapEvidence, error) {
	empty := store.SessionBootstrapEvidence{}
	if s == nil || ctx == nil || ctx.Err() != nil || export == nil || !validMaterialBinding(binding) || privateMaterialDirectory(s.root) != nil {
		return empty, errBootstrapBackup
	}
	dir := s.directory(binding)
	if os.Mkdir(dir, 0700) != nil {
		return empty, errBootstrapBackup
	}
	if syncMaterialDirectory(s.root) != nil {
		return empty, errBootstrapBackup
	}
	identity := bootstrapBackupManifest{Version: 1, Policy: "bootstrap-archive-v1", Binding: materialBinding(binding)}
	intent, _ := json.Marshal(identity)
	if writeMaterialExclusive(filepath.Join(dir, "intent.json"), intent) != nil {
		return empty, errBootstrapBackup
	}
	file := filepath.Join(dir, "archive.tar")
	archive, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return empty, errBootstrapBackup
	}
	writer := &bootstrapBackupWriter{ctx: ctx, file: archive}
	result, exportErr := export(ctx, binding, writer)
	syncErr := archive.Sync()
	closeErr := archive.Close()
	if exportErr != nil || syncErr != nil || closeErr != nil || writer.failed || ctx.Err() != nil || result.Bytes != writer.bytes || !bootstrapDigestPattern.MatchString(result.SHA256) || !bootstrapDigestPattern.MatchString(result.IsolationSHA256) {
		return empty, errBootstrapBackup
	}
	actual, bytes, policy, err := verifyBootstrapBackupArchiveAny(ctx, file)
	if err != nil || actual != result.SHA256 || bytes != result.Bytes {
		return empty, errBootstrapBackup
	}
	identity.Policy, _ = bootstrapArchivePolicyName(policy)
	identity.Export = result
	complete, _ := json.Marshal(identity)
	if writeMaterialExclusive(filepath.Join(dir, "complete.json"), complete) != nil {
		return empty, errBootstrapBackup
	}
	return s.observedEvidence(ctx, binding)
}

func (s *bootstrapBackupStore) observe(ctx context.Context, binding store.SessionRuntimeBinding) (store.SessionBootstrapEvidence, bool, error) {
	evidence, err := s.observedEvidence(ctx, binding)
	return evidence, err == nil, err
}

func (s *bootstrapBackupStore) observePolicy(ctx context.Context, binding store.SessionRuntimeBinding, expected bootstrapArchivePolicy) (store.SessionBootstrapEvidence, bool, error) {
	if _, ok := bootstrapArchivePolicyName(expected); !ok {
		return store.SessionBootstrapEvidence{}, false, errBootstrapBackup
	}
	evidence, err := s.observedEvidenceForPolicy(ctx, binding, &expected)
	return evidence, err == nil, err
}

func (s *bootstrapBackupStore) observedEvidence(ctx context.Context, binding store.SessionRuntimeBinding) (store.SessionBootstrapEvidence, error) {
	return s.observedEvidenceForPolicy(ctx, binding, nil)
}

func (s *bootstrapBackupStore) observedEvidenceForPolicy(ctx context.Context, binding store.SessionRuntimeBinding, expected *bootstrapArchivePolicy) (store.SessionBootstrapEvidence, error) {
	empty := store.SessionBootstrapEvidence{}
	if s == nil || ctx == nil || ctx.Err() != nil || !validMaterialBinding(binding) || privateMaterialDirectory(s.root) != nil {
		return empty, errBootstrapBackup
	}
	dir := s.directory(binding)
	if privateMaterialDirectory(dir) != nil {
		return empty, errBootstrapBackup
	}
	intentData, err := readMaterialFile(filepath.Join(dir, "intent.json"), 4096)
	if err != nil {
		return empty, errBootstrapBackup
	}
	completeData, err := readMaterialFile(filepath.Join(dir, "complete.json"), 4096)
	if err != nil {
		return empty, errBootstrapBackup
	}
	var intent, complete bootstrapBackupManifest
	if json.Unmarshal(intentData, &intent) != nil || json.Unmarshal(completeData, &complete) != nil || intent.Version != 1 || complete.Version != 1 || (intent.Policy != "bootstrap-archive-v1" && intent.Policy != complete.Policy) || !validBootstrapArchivePolicyName(complete.Policy) || intent.Binding != materialBinding(binding) || complete.Binding != intent.Binding || intent.Export != (bootstrapTLSArchiveEvidence{}) || !bootstrapDigestPattern.MatchString(complete.Export.SHA256) || !bootstrapDigestPattern.MatchString(complete.Export.IsolationSHA256) {
		return empty, errBootstrapBackup
	}
	policy, _ := bootstrapArchivePolicyFromName(complete.Policy)
	if expected != nil && policy != *expected {
		return empty, errBootstrapBackup
	}
	actual, bytes, err := verifyBootstrapBackupArchiveWithPolicy(ctx, filepath.Join(dir, "archive.tar"), policy)
	if err != nil || ctx.Err() != nil || actual != complete.Export.SHA256 || bytes != complete.Export.Bytes {
		return empty, errBootstrapBackup
	}
	canonical, _ := json.Marshal(complete)
	return store.SessionBootstrapEvidence{SHA256: materialDigest(canonical)}, nil
}

func validBootstrapArchivePolicyName(name string) bool {
	_, ok := bootstrapArchivePolicyFromName(name)
	return ok
}

type bootstrapBackupWriter struct {
	ctx    context.Context
	file   *os.File
	bytes  int64
	failed bool
}

func (w *bootstrapBackupWriter) Write(data []byte) (int, error) {
	if w.failed || w.ctx.Err() != nil || int64(len(data)) > bootstrapArchiveMaxBytes-w.bytes {
		w.failed = true
		return 0, errBootstrapBackup
	}
	n, err := w.file.Write(data)
	w.bytes += int64(n)
	if err != nil || n != len(data) {
		w.failed = true
		return n, errBootstrapBackup
	}
	return n, nil
}

type bootstrapBackupReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r bootstrapBackupReader) Read(data []byte) (int, error) {
	if r.ctx.Err() != nil {
		return 0, errBootstrapBackup
	}
	return r.reader.Read(data)
}

func verifyBootstrapBackupArchive(ctx context.Context, file string) (string, int64, error) {
	actual, bytes, _, err := verifyBootstrapBackupArchivePolicies(ctx, file, bootstrapArchivePolicyTLS)
	return actual, bytes, err
}

func verifyBootstrapBackupArchiveAny(ctx context.Context, file string) (string, int64, bootstrapArchivePolicy, error) {
	return verifyBootstrapBackupArchivePolicies(ctx, file, bootstrapArchivePolicyTLS, bootstrapArchivePolicyK3sCredentials, bootstrapArchivePolicySessionImageCredentials, bootstrapArchivePolicySessionImageCredentialsV2)
}

func verifyBootstrapBackupArchiveWithPolicy(ctx context.Context, file string, policy bootstrapArchivePolicy) (string, int64, error) {
	actual, bytes, _, err := verifyBootstrapBackupArchivePolicies(ctx, file, policy)
	return actual, bytes, err
}

func verifyBootstrapBackupArchivePolicies(ctx context.Context, file string, policies ...bootstrapArchivePolicy) (string, int64, bootstrapArchivePolicy, error) {
	if ctx == nil || ctx.Err() != nil || noMaterialSymlinks(file) != nil {
		return "", 0, 0, errBootstrapBackup
	}
	before, err := os.Lstat(file)
	if err != nil || !before.Mode().IsRegular() || before.Mode().Perm()&0077 != 0 || !materialOwnerTrusted(before) || before.Size() <= 0 || before.Size() > bootstrapArchiveMaxBytes {
		return "", 0, 0, errBootstrapBackup
	}
	archive, err := os.Open(file)
	if err != nil {
		return "", 0, 0, errBootstrapBackup
	}
	defer archive.Close()
	opened, err := archive.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return "", 0, 0, errBootstrapBackup
	}
	digest := sha256.New()
	bytes, err := io.Copy(digest, io.LimitReader(bootstrapBackupReader{ctx, archive}, bootstrapArchiveMaxBytes+1))
	if err != nil || bytes != before.Size() {
		return "", 0, 0, errBootstrapBackup
	}
	var verifiedPolicy bootstrapArchivePolicy
	for _, policy := range policies {
		if _, ok := bootstrapArchivePolicyName(policy); !ok {
			return "", 0, 0, errBootstrapBackup
		}
		if _, err := archive.Seek(0, io.SeekStart); err != nil {
			return "", 0, 0, errBootstrapBackup
		}
		if validateBootstrapSanitationArchive(bootstrapBackupReader{ctx, archive}, policy) == nil {
			verifiedPolicy = policy
			break
		}
	}
	if verifiedPolicy == 0 {
		return "", 0, 0, errBootstrapBackup
	}
	after, err := archive.Stat()
	if err != nil || ctx.Err() != nil || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		return "", 0, 0, errBootstrapBackup
	}
	return hex.EncodeToString(digest.Sum(nil)), bytes, verifiedPolicy, nil
}
