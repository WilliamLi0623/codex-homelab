package codexsession

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/netip"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

var p28HostKeyAliasPattern = regexp.MustCompile(`^p28-session-[0-9a-f]{64}$`)
var p28ArtifactDigestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var safeRemotePathPattern = regexp.MustCompile(`^/[A-Za-z0-9._/-]+$`)

// SSHAppServerConfig contains the generation-bound connection material for one
// pinned App Server. User, remote port, SSH options, and the remote executable
// are deliberately fixed by this transport.
type SSHAppServerConfig struct {
	SSHExecutable  string
	Address        string
	IdentityFile   string
	KnownHostsFile string
	HostKeyAlias   string
	CodexHome      string
}

type sshPathKind int

const (
	sshPathDirectoryAncestor sshPathKind = iota
	sshPathExecutable
	sshPathPrivateMaterial
)

// BuildSSHAppServerCommand validates a pinned SSH configuration and returns
// the exact local executable, OpenSSH argument vector, and sanitized local
// environment used by StartSSHAppServerProcess.
func BuildSSHAppServerCommand(config SSHAppServerConfig, localEnvironment []string) (string, []string, []string, error) {
	if err := validateSSHAppServerConfig(config); err != nil {
		return "", nil, nil, err
	}
	environment := buildSSHLocalEnvironment(localEnvironment)
	args := []string{
		"-F", "/dev/null",
		"-T", "-a", "-x",
		"-oBatchMode=yes",
		"-oStrictHostKeyChecking=yes",
		"-oIdentitiesOnly=yes",
		"-oIdentityAgent=none",
		"-oGlobalKnownHostsFile=/dev/null",
		"-oUserKnownHostsFile=" + config.KnownHostsFile,
		"-oHostKeyAlias=" + config.HostKeyAlias,
		"-oForwardAgent=no",
		"-oClearAllForwardings=yes",
		"-oPasswordAuthentication=no",
		"-oKbdInteractiveAuthentication=no",
		"-oNumberOfPasswordPrompts=0",
		"-oConnectTimeout=10",
		"-oServerAliveInterval=15",
		"-oServerAliveCountMax=3",
		"-i", config.IdentityFile,
		"--", "root@" + config.Address,
		"/usr/bin/env", "-i",
		"HOME=/root",
		"CODEX_HOME=" + config.CodexHome,
		"PATH=/usr/local/bin:/usr/bin:/bin",
		"LANG=C.UTF-8",
		"/usr/local/bin/codex", "app-server", "--listen", "stdio://",
	}
	return config.SSHExecutable, args, environment, nil
}

// StartSSHAppServerProcess starts the pinned remote App Server over OpenSSH
// from the Linux Controller. Portable command construction is not evidence of
// Windows ACL/path support; non-Linux launch is explicitly rejected.
// The remote process receives only the fixed command and an explicit clean
// environment; stdin stays open for JSON-RPC until the shared process
// lifecycle closes it.
func StartSSHAppServerProcess(ctx context.Context, config SSHAppServerConfig) (*AppServerProcess, error) {
	if runtime.GOOS != "linux" {
		return nil, errors.New("pinned SSH App Server transport requires a Linux Controller")
	}
	if ctx == nil {
		return nil, errors.New("SSH App Server context is required")
	}
	executable, args, environment, err := BuildSSHAppServerCommand(config, os.Environ())
	if err != nil {
		return nil, err
	}
	return startAppServerCommand(ctx, executable, args, environment)
}

// BuildSSHArtifactInstallCommand returns the fixed, pinned SSH invocation for
// the one-shot Codex bundle receiver. The only remote argument is a lowercase
// generation-bound digest; archive bytes travel on stdin, never in argv/env.
func BuildSSHArtifactInstallCommand(config SSHAppServerConfig, generationDigest string, localEnvironment []string) (string, []string, []string, error) {
	return buildSSHArtifactCommand(config, []string{generationDigest}, localEnvironment)
}

// BuildSSHArtifactObserveCommand constructs the fixed read-only reconciliation
// command. It cannot accept a shell fragment or an arbitrary remote path.
func BuildSSHArtifactObserveCommand(config SSHAppServerConfig, generationDigest string, localEnvironment []string) (string, []string, []string, error) {
	return buildSSHArtifactCommand(config, []string{"--observe", generationDigest}, localEnvironment)
}

func buildSSHArtifactCommand(config SSHAppServerConfig, remoteArguments, localEnvironment []string) (string, []string, []string, error) {
	if len(remoteArguments) == 0 || !p28ArtifactDigestPattern.MatchString(remoteArguments[len(remoteArguments)-1]) || (len(remoteArguments) == 2 && remoteArguments[0] != "--observe") || len(remoteArguments) > 2 {
		return "", nil, nil, errors.New("Codex artifact command arguments are invalid")
	}
	if err := validateSSHAppServerConfig(config); err != nil {
		return "", nil, nil, err
	}
	args := []string{
		"-F", "/dev/null",
		"-T", "-a", "-x",
		"-oBatchMode=yes",
		"-oStrictHostKeyChecking=yes",
		"-oIdentitiesOnly=yes",
		"-oIdentityAgent=none",
		"-oGlobalKnownHostsFile=/dev/null",
		"-oUserKnownHostsFile=" + config.KnownHostsFile,
		"-oHostKeyAlias=" + config.HostKeyAlias,
		"-oForwardAgent=no",
		"-oClearAllForwardings=yes",
		"-oPasswordAuthentication=no",
		"-oKbdInteractiveAuthentication=no",
		"-oNumberOfPasswordPrompts=0",
		"-oConnectTimeout=10",
		"-oServerAliveInterval=15",
		"-oServerAliveCountMax=3",
		"-i", config.IdentityFile,
		"--", "root@" + config.Address,
		"/usr/local/libexec/codex-artifact-install",
	}
	args = append(args, remoteArguments...)
	return config.SSHExecutable, args, buildSSHLocalEnvironment(localEnvironment), nil
}

// RunSSHArtifactInstall sends one framed artifact stream and never retries.
// All remote diagnostics are discarded so they cannot disclose paths or
// guest output; callers receive only a stable error class.
func RunSSHArtifactInstall(ctx context.Context, config SSHAppServerConfig, generationDigest string, archive io.Reader) error {
	_, err := RunSSHArtifactInstallWithEvidence(ctx, config, generationDigest, archive)
	return err
}

// RunSSHArtifactInstallWithEvidence performs exactly one pinned SSH request
// and accepts only the installer's fixed, bounded digest acknowledgment.
func RunSSHArtifactInstallWithEvidence(ctx context.Context, config SSHAppServerConfig, generationDigest string, archive io.Reader) (string, error) {
	if runtime.GOOS != "linux" || ctx == nil || archive == nil {
		return "", errors.New("pinned Codex artifact transfer requires a Linux Controller and stream")
	}
	executable, args, environment, err := BuildSSHArtifactInstallCommand(config, generationDigest, os.Environ())
	if err != nil {
		return "", errors.New("pinned Codex artifact transfer configuration is invalid")
	}
	command := exec.CommandContext(ctx, executable, args...)
	command.Env = environment
	command.Stdin = archive
	var stdout boundedSSHArtifactOutput
	command.Stdout = &stdout
	command.Stderr = io.Discard
	if err := command.Run(); err != nil {
		return "", errors.New("pinned Codex artifact transfer did not complete")
	}
	if stdout.overflow || !strings.HasPrefix(stdout.data.String(), "P28_CODEX_INSTALL_COMPLETE:") || !strings.HasSuffix(stdout.data.String(), "\n") {
		return "", errors.New("pinned Codex artifact transfer acknowledgment is invalid")
	}
	digest := strings.TrimSuffix(strings.TrimPrefix(stdout.data.String(), "P28_CODEX_INSTALL_COMPLETE:"), "\n")
	if !p28ArtifactDigestPattern.MatchString(digest) || strings.Count(stdout.data.String(), "\n") != 1 {
		return "", errors.New("pinned Codex artifact transfer acknowledgment is invalid")
	}
	return digest, nil
}

// RunSSHArtifactObserve reconciles a prior upload with a bounded fixed-token
// response. It performs only one read-only command and never retries.
func RunSSHArtifactObserve(ctx context.Context, config SSHAppServerConfig, generationDigest string) (string, error) {
	if runtime.GOOS != "linux" || ctx == nil {
		return "", errors.New("pinned Codex artifact observation requires a Linux Controller")
	}
	executable, args, environment, err := BuildSSHArtifactObserveCommand(config, generationDigest, os.Environ())
	if err != nil {
		return "", errors.New("pinned Codex artifact observation configuration is invalid")
	}
	command := exec.CommandContext(ctx, executable, args...)
	command.Env = environment
	command.Stdin = strings.NewReader("")
	var stdout boundedSSHArtifactOutput
	command.Stdout = &stdout
	command.Stderr = io.Discard
	if err := command.Run(); err != nil || stdout.overflow {
		return "", errors.New("pinned Codex artifact observation failed")
	}
	line := stdout.data.String()
	if !strings.HasPrefix(line, "P28_CODEX_INSTALL_COMPLETE:") || !strings.HasSuffix(line, "\n") || strings.Count(line, "\n") != 1 {
		return "", errors.New("pinned Codex artifact observation acknowledgment is invalid")
	}
	digest := strings.TrimSuffix(strings.TrimPrefix(line, "P28_CODEX_INSTALL_COMPLETE:"), "\n")
	if !p28ArtifactDigestPattern.MatchString(digest) {
		return "", errors.New("pinned Codex artifact observation acknowledgment is invalid")
	}
	return digest, nil
}

type boundedSSHArtifactOutput struct {
	data     bytes.Buffer
	overflow bool
}

func (w *boundedSSHArtifactOutput) Write(data []byte) (int, error) {
	if w.overflow || len(data) > 128-w.data.Len() {
		w.overflow = true
		return 0, errors.New("installer acknowledgment exceeds limit")
	}
	return w.data.Write(data)
}

func validateSSHAppServerConfig(config SSHAppServerConfig) error {
	address, err := netip.ParseAddr(config.Address)
	if err != nil || address.Zone() != "" || !address.IsGlobalUnicast() {
		return errors.New("SSH App Server address must be a literal unicast IP address")
	}
	if !p28HostKeyAliasPattern.MatchString(config.HostKeyAlias) {
		return errors.New("SSH App Server host key alias is invalid")
	}
	if err := validateRemoteCodexHome(config.CodexHome); err != nil {
		return err
	}
	if err := validateSSHFile(config.SSHExecutable, true, false); err != nil {
		return errors.New("SSH executable must be an existing absolute regular executable without symlink traversal")
	}
	if err := validateSSHFile(config.IdentityFile, false, true); err != nil {
		return errors.New("SSH identity must be an existing absolute private regular file without symlink traversal")
	}
	if err := validateSSHFile(config.KnownHostsFile, false, true); err != nil {
		return errors.New("SSH known_hosts pin must be an existing absolute private regular file without symlink traversal")
	}
	return nil
}

func validateRemoteCodexHome(value string) error {
	if !safeRemotePathPattern.MatchString(value) || path.Clean(value) != value || value == "/" || value == "/root" {
		return errors.New("remote CODEX_HOME must be a clean absolute POSIX path outside / and /root")
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == ".." || segment == "." {
			return errors.New("remote CODEX_HOME must not contain traversal segments")
		}
	}
	return nil
}

func validateSSHFile(file string, executable, private bool) error {
	if file == "" || !filepath.IsAbs(file) || filepath.Clean(file) != file {
		return errors.New("path must be absolute and clean")
	}
	info, err := lstatWithoutSymlinkTraversal(file)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return errors.New("file must be a non-empty regular file")
	}
	if runtime.GOOS != "windows" {
		if !sshPathOwnerTrusted(info) {
			return errors.New("SSH path owner is untrusted")
		}
		if executable {
			if err := validateSSHPathMode(info.Mode(), sshPathExecutable, false); err != nil {
				return err
			}
		}
		if private {
			if err := validateSSHPathMode(info.Mode(), sshPathPrivateMaterial, false); err != nil {
				return err
			}
		}
	}
	return nil
}

func lstatWithoutSymlinkTraversal(file string) (os.FileInfo, error) {
	clean := filepath.Clean(file)
	volume := filepath.VolumeName(clean)
	remainder := strings.TrimLeft(clean[len(volume):], string(filepath.Separator))
	current := volume + string(filepath.Separator)
	if volume == "" && !filepath.IsAbs(clean) {
		return nil, errors.New("path is not absolute")
	}
	parts := strings.Split(remainder, string(filepath.Separator))
	for index, part := range parts {
		if part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("path traverses a symbolic link")
		}
		if index < len(parts)-1 && !info.IsDir() {
			return nil, errors.New("path parent is not a directory")
		}
		if runtime.GOOS != "windows" {
			if !sshPathOwnerTrusted(info) {
				return nil, errors.New("path owner is untrusted")
			}
			if index < len(parts)-1 {
				if err := validateSSHPathMode(info.Mode(), sshPathDirectoryAncestor, trustedStickySSHAncestor(current)); err != nil {
					return nil, err
				}
			}
		}
		if index == len(parts)-1 {
			return info, nil
		}
	}
	return nil, errors.New("path does not name a file")
}

func validateSSHPathMode(mode os.FileMode, kind sshPathKind, trustedStickyTemp bool) error {
	switch kind {
	case sshPathDirectoryAncestor:
		if !mode.IsDir() {
			return errors.New("SSH path ancestor is not a directory")
		}
		if mode.Perm()&0o022 != 0 && !(trustedStickyTemp && mode&os.ModeSticky != 0) {
			return errors.New("SSH path ancestor is writable by group or others")
		}
	case sshPathExecutable:
		if mode.Perm()&0o111 == 0 {
			return errors.New("SSH executable has no execute permission")
		}
		if mode.Perm()&0o022 != 0 {
			return errors.New("SSH executable is writable by group or others")
		}
	case sshPathPrivateMaterial:
		if mode.Perm()&0o077 != 0 {
			return errors.New("SSH private material is accessible by group or others")
		}
	default:
		return errors.New("unknown SSH path kind")
	}
	return nil
}

func trustedStickySSHAncestor(value string) bool {
	clean := filepath.Clean(value)
	return clean == string(filepath.Separator)+"tmp" || clean == filepath.Join(string(filepath.Separator), "var", "tmp")
}

func buildSSHLocalEnvironment(input []string) []string {
	allowed := map[string]bool{"PATH": true, "LANG": true, "LC_ALL": true}
	if runtime.GOOS == "windows" {
		allowed["SYSTEMROOT"] = true
		allowed["WINDIR"] = true
		allowed["TEMP"] = true
		allowed["TMP"] = true
	}
	result := make([]string, 0, len(allowed))
	seen := make(map[string]bool, len(allowed))
	for _, entry := range input {
		key, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		key = strings.ToUpper(key)
		if allowed[key] && !seen[key] {
			result = append(result, entry)
			seen[key] = true
		}
	}
	return result
}
