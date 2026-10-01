package sessionruntime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/WilliamLi0623/codex-homelab/internal/store"
)

var (
	ErrSSHMaterialInvalid = errors.New("Session SSH material is invalid or untrusted")
	ErrSSHMaterialExists  = errors.New("Session SSH material already exists; observe without replacement")
	ErrSSHMaterialUnknown = errors.New("Session SSH material preparation outcome is unknown")
)

// SSHMaterial is Controller-side transport material, never browser/API data.
// It does not establish selected-account identity or runtime readiness.
type SSHMaterial struct {
	IdentityFile    string
	KnownHostsFile  string
	Alias           string
	ClientPublicKey string
}

type SSHMaterialRegistry struct{ root, keygen string }
type sshMaterialManifest struct {
	Version         int                         `json:"version"`
	Binding         store.SessionRuntimeBinding `json:"binding"`
	ClientPublicKey string                      `json:"client_public_key"`
	PrivateSHA256   string                      `json:"private_sha256"`
}

// The root must be an existing private Controller directory. No default store,
// template key reuse, implicit import or credential propagation is provided.
func NewSSHMaterialRegistry(root, keygen string) (*SSHMaterialRegistry, error) {
	if runtime.GOOS != "linux" || !filepath.IsAbs(root) || filepath.Clean(root) != root || root == string(filepath.Separator) || !filepath.IsAbs(keygen) || filepath.Clean(keygen) != keygen {
		return nil, ErrSSHMaterialInvalid
	}
	if err := privateMaterialDirectory(root); err != nil {
		return nil, err
	}
	if err := noMaterialSymlinks(keygen); err != nil {
		return nil, err
	}
	info, err := os.Lstat(keygen)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 || info.Mode().Perm()&0111 == 0 || !materialOwnerTrusted(info) {
		return nil, ErrSSHMaterialInvalid
	}
	return &SSHMaterialRegistry{root: root, keygen: keygen}, nil
}

// Prepare creates one exclusive generation directory before keygen. A failed
// operation leaves the intent directory intact and is NEVER retried/cleaned.
// Caller must already hold a fresh durable bootstrap claim.
func (r *SSHMaterialRegistry) Prepare(ctx context.Context, binding store.SessionRuntimeBinding) (SSHMaterial, error) {
	var empty SSHMaterial
	if r == nil || ctx == nil || !validMaterialBinding(binding) {
		return empty, ErrSSHMaterialInvalid
	}
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	if err := privateMaterialDirectory(r.root); err != nil {
		return empty, err
	}
	dir := r.directory(binding)
	if err := os.Mkdir(dir, 0700); err != nil {
		if errors.Is(err, os.ErrExist) {
			return empty, ErrSSHMaterialExists
		}
		return empty, ErrSSHMaterialUnknown
	}
	if err := syncMaterialDirectory(r.root); err != nil {
		return empty, ErrSSHMaterialUnknown
	}
	keyCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	identity := filepath.Join(dir, "client")
	cmd := exec.CommandContext(keyCtx, r.keygen, "-q", "-t", "ed25519", "-N", "", "-f", identity, "-C", r.alias(binding))
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C.UTF-8"}
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return empty, ErrSSHMaterialUnknown
	}
	if err := os.Chmod(identity+".pub", 0600); err != nil {
		return empty, ErrSSHMaterialUnknown
	}
	private, err := readMaterialFile(identity, 256*1024)
	if err != nil {
		return empty, ErrSSHMaterialUnknown
	}
	pub, err := readMaterialFile(identity+".pub", 4096)
	if err != nil {
		return empty, ErrSSHMaterialUnknown
	}
	// keygen's comment is controlled; pin files themselves contain no comments.
	fields := strings.Fields(string(pub))
	if len(fields) != 3 || fields[2] != r.alias(binding) {
		return empty, ErrSSHMaterialUnknown
	}
	public := strings.Join(fields[:2], " ")
	if !validMaterialPublicKey(public) {
		return empty, ErrSSHMaterialUnknown
	}
	manifest := sshMaterialManifest{Version: 1, Binding: materialBinding(binding), ClientPublicKey: public, PrivateSHA256: materialDigest(private)}
	data, err := json.Marshal(manifest)
	if err != nil || writeMaterialExclusive(filepath.Join(dir, "material.json"), data) != nil {
		return empty, ErrSSHMaterialUnknown
	}
	return r.readPrepared(binding)
}

// Pin's caller MUST obtain the public key from the authenticated PVE console
// while networking is fenced. This filesystem registry cannot prove its source;
// the production driver must enforce and test that boundary before enablement.
// Pin never scans a network endpoint or replaces an existing pin.
func (r *SSHMaterialRegistry) Pin(binding store.SessionRuntimeBinding, hostPublicKey string) error {
	material, err := r.readPrepared(binding)
	if err != nil {
		return err
	}
	if !validMaterialPublicKey(hostPublicKey) || hostPublicKey == material.ClientPublicKey {
		return ErrSSHMaterialInvalid
	}
	data := []byte(material.Alias + " " + hostPublicKey + "\n")
	if err := writeMaterialExclusive(material.KnownHostsFile, data); err != nil {
		return err
	}
	// Commit marker separate from the SSH-readable file detects accidental pin
	// substitution. A partial Pin fails closed; it is not repaired or retried.
	if err := writeMaterialExclusive(filepath.Join(filepath.Dir(material.IdentityFile), "pin.sha256"), []byte(materialDigest(data)+"\n")); err != nil {
		return ErrSSHMaterialUnknown
	}
	if _, err := r.Load(binding); err != nil {
		return ErrSSHMaterialUnknown
	}
	return nil
}

// Load is read-only and rejects absent or changed pins/material. Reopening the
// registry does not rotate keys, retry keygen, or establish account readiness.
func (r *SSHMaterialRegistry) Load(binding store.SessionRuntimeBinding) (SSHMaterial, error) {
	material, err := r.readPrepared(binding)
	if err != nil {
		return SSHMaterial{}, err
	}
	data, err := readMaterialFile(material.KnownHostsFile, 4096)
	if err != nil {
		return SSHMaterial{}, err
	}
	prefix := material.Alias + " "
	text := string(data)
	if !strings.HasPrefix(text, prefix) || !strings.HasSuffix(text, "\n") {
		return SSHMaterial{}, ErrSSHMaterialInvalid
	}
	public := strings.TrimSuffix(strings.TrimPrefix(text, prefix), "\n")
	if !validMaterialPublicKey(public) || public == material.ClientPublicKey {
		return SSHMaterial{}, ErrSSHMaterialInvalid
	}
	expected, err := readMaterialFile(filepath.Join(filepath.Dir(material.IdentityFile), "pin.sha256"), 128)
	if err != nil || string(expected) != materialDigest(data)+"\n" {
		return SSHMaterial{}, ErrSSHMaterialInvalid
	}
	return material, nil
}

func (r *SSHMaterialRegistry) readPrepared(binding store.SessionRuntimeBinding) (SSHMaterial, error) {
	var empty SSHMaterial
	if r == nil || !validMaterialBinding(binding) || privateMaterialDirectory(r.root) != nil {
		return empty, ErrSSHMaterialInvalid
	}
	dir := r.directory(binding)
	if privateMaterialDirectory(dir) != nil {
		return empty, ErrSSHMaterialInvalid
	}
	data, err := readMaterialFile(filepath.Join(dir, "material.json"), 8192)
	if err != nil {
		return empty, err
	}
	var manifest sshMaterialManifest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&manifest) != nil {
		return empty, ErrSSHMaterialInvalid
	}
	var tail any
	if decoder.Decode(&tail) != io.EOF || manifest.Version != 1 || manifest.Binding != materialBinding(binding) || !validMaterialPublicKey(manifest.ClientPublicKey) {
		return empty, ErrSSHMaterialInvalid
	}
	identity := filepath.Join(dir, "client")
	private, err := readMaterialFile(identity, 256*1024)
	if err != nil || materialDigest(private) != manifest.PrivateSHA256 {
		return empty, ErrSSHMaterialInvalid
	}
	pub, err := readMaterialFile(identity+".pub", 4096)
	if err != nil {
		return empty, ErrSSHMaterialInvalid
	}
	if string(pub) != manifest.ClientPublicKey+" "+r.alias(binding)+"\n" {
		return empty, ErrSSHMaterialInvalid
	}
	return SSHMaterial{IdentityFile: identity, KnownHostsFile: filepath.Join(dir, "known_hosts"), Alias: r.alias(binding), ClientPublicKey: manifest.ClientPublicKey}, nil
}

func materialBinding(binding store.SessionRuntimeBinding) store.SessionRuntimeBinding {
	return store.SessionRuntimeBinding{ID: binding.ID, SessionID: binding.SessionID, EpochID: binding.EpochID, Generation: binding.Generation, VMID: binding.VMID}
}

func validMaterialBinding(binding store.SessionRuntimeBinding) bool {
	if !validBootstrapBinding(binding) {
		return false
	}
	for _, value := range []string{binding.ID, binding.SessionID, binding.EpochID, binding.Generation} {
		if len(value) > 256 || strings.TrimSpace(value) != value || strings.ContainsAny(value, "\x00\r\n") {
			return false
		}
	}
	return true
}

func (r *SSHMaterialRegistry) alias(binding store.SessionRuntimeBinding) string {
	data, _ := json.Marshal(materialBinding(binding))
	return "p28-session-" + materialDigest(data)
}
func (r *SSHMaterialRegistry) directory(binding store.SessionRuntimeBinding) string {
	return filepath.Join(r.root, strings.TrimPrefix(r.alias(binding), "p28-session-"))
}
func materialDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func validMaterialPublicKey(key string) bool {
	parts := strings.Split(key, " ")
	if len(parts) != 2 || parts[0] != "ssh-ed25519" || strings.ContainsAny(key, "\r\n\t") {
		return false
	}
	blob, err := base64.StdEncoding.Strict().DecodeString(parts[1])
	return err == nil && len(blob) == 51 && binary.BigEndian.Uint32(blob[:4]) == 11 && string(blob[4:15]) == "ssh-ed25519" && binary.BigEndian.Uint32(blob[15:19]) == 32 && base64.StdEncoding.EncodeToString(blob) == parts[1]
}

func noMaterialSymlinks(path string) error {
	for current := path; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return ErrSSHMaterialInvalid
		}
		// Keep the entire chain within Controller/root ownership. Sticky temp
		// roots protect entries owned by another UID; ordinary writable parents
		// do not. A compromised Controller UID/root is outside this boundary.
		if !materialOwnerTrusted(info) || (info.IsDir() && info.Mode().Perm()&0022 != 0 && info.Mode()&os.ModeSticky == 0) {
			return ErrSSHMaterialInvalid
		}
		if filepath.Dir(current) == current {
			return nil
		}
	}
}
func privateMaterialDirectory(path string) error {
	if noMaterialSymlinks(path) != nil {
		return ErrSSHMaterialInvalid
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 || !materialOwnerTrusted(info) {
		return ErrSSHMaterialInvalid
	}
	return nil
}
func readMaterialFile(path string, limit int64) ([]byte, error) {
	if noMaterialSymlinks(path) != nil {
		return nil, ErrSSHMaterialInvalid
	}
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Mode().Perm()&0077 != 0 || !materialOwnerTrusted(before) {
		return nil, ErrSSHMaterialInvalid
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, ErrSSHMaterialInvalid
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return nil, ErrSSHMaterialInvalid
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(data)) > limit || len(data) == 0 {
		return nil, ErrSSHMaterialInvalid
	}
	return data, nil
}
func writeMaterialExclusive(path string, data []byte) error {
	if privateMaterialDirectory(filepath.Dir(path)) != nil {
		return ErrSSHMaterialInvalid
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if errors.Is(err, os.ErrExist) {
		return ErrSSHMaterialExists
	}
	if err != nil {
		return ErrSSHMaterialUnknown
	}
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		return ErrSSHMaterialUnknown
	}
	return syncMaterialDirectory(filepath.Dir(path))
}

func syncMaterialDirectory(path string) error {
	if privateMaterialDirectory(path) != nil {
		return ErrSSHMaterialInvalid
	}
	dir, err := os.Open(path)
	if err != nil {
		return ErrSSHMaterialUnknown
	}
	defer dir.Close()
	if dir.Sync() != nil {
		return ErrSSHMaterialUnknown
	}
	return nil
}
