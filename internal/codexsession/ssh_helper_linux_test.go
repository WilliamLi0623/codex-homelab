package codexsession

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const sshHelperTestDigest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestSSHBootstrapHelperInstallAndObserveUseVerifiedTemporaryLayout(t *testing.T) {
	requireLinuxRoot(t)
	config := newSSHBootstrapHelperTestConfig(t)
	layout := newSSHBootstrapHelperTestLayout(t)
	body := []byte("#!/bin/sh\nprintf 'bootstrap helper ran\\n'\n")
	helperHash := fmt.Sprintf("%x", sha256.Sum256(body))

	got, err := runSSHBootstrapHelperInstallWithLayout(context.Background(), config, sshHelperTestDigest, helperHash, body, layout)
	if err != nil || got != sshHelperTestDigest {
		t.Fatalf("install = %q, %v", got, err)
	}
	file := filepath.Join(layout.root, sshHelperTestDigest, "codex-artifact-install")
	installed, err := os.ReadFile(file)
	if err != nil || string(installed) != string(body) {
		t.Fatalf("installed file = %q, %v", installed, err)
	}
	if info, err := os.Stat(file); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("installed helper mode = %v, %v; want 0700", info, err)
	}
	if target, err := os.Readlink(layout.launcher); err != nil || target != file {
		t.Fatalf("launcher target = %q, %v; want %q", target, err, file)
	}

	got, err = runSSHBootstrapHelperObserveWithLayout(context.Background(), config, sshHelperTestDigest, helperHash, int64(len(body)), layout)
	if err != nil || got != sshHelperTestDigest {
		t.Fatalf("observe = %q, %v", got, err)
	}
}

func TestSSHBootstrapHelperInstallCreatesMissingTrustedDirectories(t *testing.T) {
	requireLinuxRoot(t)
	config := newSSHBootstrapHelperTestConfig(t)
	base := t.TempDir()
	opt := filepath.Join(base, "opt")
	usrLocal := filepath.Join(base, "usr", "local")
	for _, dir := range []string{opt, usrLocal} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	layout := sshBootstrapHelperLayout{
		root:     filepath.Join(opt, "codex-bootstrap-helper"),
		launcher: filepath.Join(usrLocal, "libexec", "codex-artifact-install"),
	}
	if _, err := os.Lstat(layout.root); !os.IsNotExist(err) {
		t.Fatalf("test helper root should start absent: %v", err)
	}
	if _, err := os.Lstat(filepath.Dir(layout.launcher)); !os.IsNotExist(err) {
		t.Fatalf("test launcher parent should start absent: %v", err)
	}
	body := []byte("#!/bin/sh\nprintf 'created from verified bootstrap\\n'\n")
	helperHash := fmt.Sprintf("%x", sha256.Sum256(body))
	if _, err := runSSHBootstrapHelperInstallWithLayout(context.Background(), config, sshHelperTestDigest, helperHash, body, layout); err != nil {
		t.Fatalf("install into missing trusted directories: %v", err)
	}
	for _, dir := range []string{layout.root, filepath.Dir(layout.launcher)} {
		info, err := os.Stat(dir)
		if err != nil || info.Mode().Perm() != 0o700 {
			t.Fatalf("created directory %q mode = %v, %v; want 0700", dir, info, err)
		}
	}
	if _, err := runSSHBootstrapHelperObserveWithLayout(context.Background(), config, sshHelperTestDigest, helperHash, int64(len(body)), layout); err != nil {
		t.Fatalf("observe installed helper after directory creation: %v", err)
	}
}

func TestSSHBootstrapHelperObserveDoesNotCreateMissingDirectories(t *testing.T) {
	requireLinuxRoot(t)
	config := newSSHBootstrapHelperTestConfig(t)
	base := t.TempDir()
	opt := filepath.Join(base, "opt")
	usrLocal := filepath.Join(base, "usr", "local")
	for _, dir := range []string{opt, usrLocal} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	layout := sshBootstrapHelperLayout{
		root:     filepath.Join(opt, "codex-bootstrap-helper"),
		launcher: filepath.Join(usrLocal, "libexec", "codex-artifact-install"),
	}
	if _, err := runSSHBootstrapHelperObserveWithLayout(context.Background(), config, sshHelperTestDigest, sshHelperTestDigest, 1, layout); err == nil {
		t.Fatal("observe accepted an absent installation")
	}
	for _, dir := range []string{layout.root, filepath.Dir(layout.launcher)} {
		if _, err := os.Lstat(dir); !os.IsNotExist(err) {
			t.Fatalf("read-only observe created directory %q: %v", dir, err)
		}
	}
}

func TestSSHBootstrapHelperInstallNeverReplacesExistingGeneration(t *testing.T) {
	requireLinuxRoot(t)
	config := newSSHBootstrapHelperTestConfig(t)
	layout := newSSHBootstrapHelperTestLayout(t)
	generation := filepath.Join(layout.root, sshHelperTestDigest)
	if err := os.Mkdir(generation, 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(generation, "codex-artifact-install")
	if err := os.WriteFile(file, []byte("previous stage"), 0o600); err != nil {
		t.Fatal(err)
	}
	body := []byte("replacement")
	helperHash := fmt.Sprintf("%x", sha256.Sum256(body))
	if _, err := runSSHBootstrapHelperInstallWithLayout(context.Background(), config, sshHelperTestDigest, helperHash, body, layout); err == nil {
		t.Fatal("install replaced an existing generation")
	}
	got, err := os.ReadFile(file)
	if err != nil || string(got) != "previous stage" {
		t.Fatalf("existing staged file = %q, %v", got, err)
	}
}

func TestSSHBootstrapHelperInstallNeverReplacesExistingLauncher(t *testing.T) {
	requireLinuxRoot(t)
	config := newSSHBootstrapHelperTestConfig(t)
	layout := newSSHBootstrapHelperTestLayout(t)
	if err := os.WriteFile(layout.launcher, []byte("existing launcher"), 0o600); err != nil {
		t.Fatal(err)
	}
	body := []byte("new helper")
	helperHash := fmt.Sprintf("%x", sha256.Sum256(body))
	if _, err := runSSHBootstrapHelperInstallWithLayout(context.Background(), config, sshHelperTestDigest, helperHash, body, layout); err == nil {
		t.Fatal("install replaced an existing launcher")
	}
	got, err := os.ReadFile(layout.launcher)
	if err != nil || string(got) != "existing launcher" {
		t.Fatalf("existing launcher = %q, %v", got, err)
	}
	staged := filepath.Join(layout.root, sshHelperTestDigest, "codex-artifact-install")
	if info, err := os.Stat(staged); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("failed exclusive publish changed its verified stage: %v, %v", info, err)
	}
}

func TestSSHBootstrapHelperRejectsBadHashAndOversizeBeforeStartingSSH(t *testing.T) {
	requireLinuxRoot(t)
	config := newSSHBootstrapHelperTestConfig(t)
	marker := filepath.Join(t.TempDir(), "ssh-started")
	config.SSHExecutable = writeSSHBootstrapHelperFake(t, marker, "")
	body := []byte("body")
	if _, err := RunSSHBootstrapHelperInstall(context.Background(), config, sshHelperTestDigest, strings.Repeat("a", 64), body); err == nil {
		t.Fatal("body with a mismatched hash was accepted")
	}
	oversize := make([]byte, sshBootstrapHelperMaxBytes+1)
	if _, err := RunSSHBootstrapHelperInstall(context.Background(), config, sshHelperTestDigest, fmt.Sprintf("%x", sha256.Sum256(oversize)), oversize); err == nil {
		t.Fatal("oversize helper body was accepted")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("SSH process started before local validation: stat error = %v", err)
	}
}

func TestSSHBootstrapReceiverRetainsPartialFileAndRejectsTruncatedExtraAndBadHash(t *testing.T) {
	requireLinuxRoot(t)
	for _, tc := range []struct {
		name     string
		declared int
		body     []byte
		hash     string
	}{
		{name: "truncated", declared: 5, body: []byte("abc"), hash: fmt.Sprintf("%x", sha256.Sum256([]byte("abc")))},
		{name: "extra", declared: 3, body: []byte("abcd"), hash: fmt.Sprintf("%x", sha256.Sum256([]byte("abc")))},
		{name: "hash mismatch", declared: 3, body: []byte("abc"), hash: strings.Repeat("0", 64)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			layout := newSSHBootstrapHelperTestLayout(t)
			script := bootstrapHelperReceiverScript(layout)
			cmd := exec.Command("/bin/sh", "-c", script, "codex-bootstrap-helper", "install", sshHelperTestDigest, tc.hash, fmt.Sprint(tc.declared))
			cmd.Stdin = strings.NewReader(string(tc.body))
			if output, err := cmd.CombinedOutput(); err == nil {
				t.Fatalf("receiver accepted invalid input; output=%q", output)
			}
			generation := filepath.Join(layout.root, sshHelperTestDigest)
			info, err := os.Stat(generation)
			if err != nil || info.Mode().Perm() != 0o700 {
				t.Fatalf("partial generation = %v, %v", info, err)
			}
			file := filepath.Join(generation, "codex-artifact-install")
			if info, err := os.Stat(file); err != nil || info.Mode().Perm()&0o111 != 0 {
				t.Fatalf("failed receiver published executable file: %v, %v", info, err)
			}
			if _, err := os.Lstat(layout.launcher); !os.IsNotExist(err) {
				t.Fatalf("failed receiver published launcher: %v", err)
			}
		})
	}
}

func TestSSHBootstrapHelperObserveRejectsSymlinkedGenerationAndWrongLauncher(t *testing.T) {
	requireLinuxRoot(t)
	for _, tc := range []string{"symlinked generation", "wrong launcher target"} {
		t.Run(tc, func(t *testing.T) {
			config := newSSHBootstrapHelperTestConfig(t)
			layout := newSSHBootstrapHelperTestLayout(t)
			body := []byte("helper")
			helperHash := fmt.Sprintf("%x", sha256.Sum256(body))
			if _, err := runSSHBootstrapHelperInstallWithLayout(context.Background(), config, sshHelperTestDigest, helperHash, body, layout); err != nil {
				t.Fatal(err)
			}
			if tc == "symlinked generation" {
				link := filepath.Join(layout.root, "linked-generation")
				if err := os.Rename(filepath.Join(layout.root, sshHelperTestDigest), link); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(link, filepath.Join(layout.root, sshHelperTestDigest)); err != nil {
					t.Fatal(err)
				}
			} else {
				other := filepath.Join(layout.root, "other", "codex-artifact-install")
				if err := os.MkdirAll(filepath.Dir(other), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(layout.launcher); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(other, layout.launcher); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := runSSHBootstrapHelperObserveWithLayout(context.Background(), config, sshHelperTestDigest, helperHash, int64(len(body)), layout); err == nil {
				t.Fatal("observe accepted an unsafe generation/launcher")
			}
		})
	}
}

func TestSSHBootstrapReceiverRejectsSymlinkAndWritableLayoutAncestors(t *testing.T) {
	requireLinuxRoot(t)
	for _, tc := range []string{"symlink ancestor", "group writable ancestor"} {
		t.Run(tc, func(t *testing.T) {
			layout := newSSHBootstrapHelperTestLayout(t)
			base := filepath.Dir(filepath.Dir(layout.root))
			if tc == "symlink ancestor" {
				real := filepath.Join(base, "real-root")
				if err := os.Mkdir(real, 0o755); err != nil {
					t.Fatal(err)
				}
				link := filepath.Join(base, "linked-root")
				if err := os.Symlink(real, link); err != nil {
					t.Fatal(err)
				}
				layout.root = filepath.Join(link, "bootstrap-helper")
			} else {
				unsafe := filepath.Join(base, "unsafe-parent")
				if err := os.Mkdir(unsafe, 0o777); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(unsafe, 0o777); err != nil {
					t.Fatal(err)
				}
				layout.root = filepath.Join(unsafe, "bootstrap-helper")
			}
			body := []byte("helper")
			script := bootstrapHelperReceiverScript(layout)
			cmd := exec.Command("/bin/sh", "-c", script, "codex-bootstrap-helper", "install", sshHelperTestDigest, fmt.Sprintf("%x", sha256.Sum256(body)), fmt.Sprint(len(body)))
			cmd.Stdin = strings.NewReader(string(body))
			if output, err := cmd.CombinedOutput(); err == nil {
				t.Fatalf("receiver accepted unsafe path ancestors; output=%q", output)
			}
			if _, err := os.Lstat(filepath.Join(layout.root, sshHelperTestDigest)); !os.IsNotExist(err) {
				t.Fatalf("receiver created a generation under an unsafe ancestor: %v", err)
			}
		})
	}
}

func TestSSHBootstrapHelperRejectsUnknownObserveAndCancellation(t *testing.T) {
	requireLinuxRoot(t)
	config := newSSHBootstrapHelperTestConfig(t)
	layout := newSSHBootstrapHelperTestLayout(t)
	body := []byte("helper")
	helperHash := fmt.Sprintf("%x", sha256.Sum256(body))
	if _, err := runSSHBootstrapHelperObserveWithLayout(context.Background(), config, sshHelperTestDigest, helperHash, int64(len(body)), layout); err == nil {
		t.Fatal("observe accepted an unknown generation")
	}
	marker := filepath.Join(t.TempDir(), "ssh-started")
	config.SSHExecutable = writeSSHBootstrapHelperFake(t, marker, "sleep 10\n")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := runSSHBootstrapHelperInstallWithLayout(ctx, config, sshHelperTestDigest, helperHash, body, layout); err == nil {
		t.Fatal("cancelled SSH bootstrap succeeded")
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("cancelled request started a process")
	}
}

func TestSSHBootstrapHelperCommandKeepsFixedReceiverAndPinnedSSHArguments(t *testing.T) {
	requireLinuxRoot(t)
	config := newSSHBootstrapHelperTestConfig(t)
	layout := newSSHBootstrapHelperTestLayout(t)
	argvPath := filepath.Join(t.TempDir(), "argv")
	config.SSHExecutable = writeSSHBootstrapHelperArgvFake(t, argvPath)
	body := []byte("helper")
	helperHash := fmt.Sprintf("%x", sha256.Sum256(body))
	if _, err := runSSHBootstrapHelperInstallWithLayout(context.Background(), config, sshHelperTestDigest, helperHash, body, layout); err != nil {
		t.Fatal(err)
	}
	argv, err := os.ReadFile(argvPath)
	if err != nil {
		t.Fatal(err)
	}
	joined := string(argv)
	for _, required := range []string{"StrictHostKeyChecking=yes", "IdentitiesOnly=yes", "IdentityAgent=none", "ForwardAgent=no", "PasswordAuthentication=no", "UserKnownHostsFile=" + config.KnownHostsFile, "HostKeyAlias=" + config.HostKeyAlias, "root@192.0.2.44", "/bin/sh", "install", sshHelperTestDigest, helperHash, fmt.Sprint(len(body))} {
		if !strings.Contains(joined, required) {
			t.Errorf("fake SSH argv omits %q: %s", required, joined)
		}
	}
	for _, untrusted := range []string{"session-id", ";touch", "OPENAI_API_KEY", "do-not-leak"} {
		if strings.Contains(joined, untrusted) {
			t.Errorf("remote SSH argv contains untrusted fragment %q: %s", untrusted, joined)
		}
	}
}

func TestSSHBootstrapHelperRejectsDuplicateOrMalformedAcknowledgmentAndCommandFailure(t *testing.T) {
	requireLinuxRoot(t)
	body := []byte("helper")
	helperHash := fmt.Sprintf("%x", sha256.Sum256(body))
	for _, tc := range []struct {
		name   string
		output string
		exit   int
	}{
		{name: "duplicate line", output: "P28_HELPER_COMPLETE:" + sshHelperTestDigest + "\nP28_HELPER_COMPLETE:" + sshHelperTestDigest + "\n"},
		{name: "extra output", output: "P28_HELPER_COMPLETE:" + sshHelperTestDigest + "\nunexpected\n"},
		{name: "missing newline", output: "P28_HELPER_COMPLETE:" + sshHelperTestDigest},
		{name: "command failure", output: "private remote diagnostic", exit: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := newSSHBootstrapHelperTestConfig(t)
			config.SSHExecutable = writeSSHBootstrapHelperOutputFake(t, tc.output, tc.exit)
			layout := newSSHBootstrapHelperTestLayout(t)
			if _, err := runSSHBootstrapHelperInstallWithLayout(context.Background(), config, sshHelperTestDigest, helperHash, body, layout); err == nil {
				t.Fatal("runner accepted malformed acknowledgment or command failure")
			}
		})
	}
}

func TestSSHBootstrapHelperPublicAPIsRejectMalformedDigestsAndSizes(t *testing.T) {
	config := newSSHBootstrapHelperTestConfig(t)
	for _, digest := range []string{"", strings.Repeat("A", 64), strings.Repeat("a", 63), strings.Repeat("a", 65)} {
		if _, err := RunSSHBootstrapHelperInstall(context.Background(), config, digest, sshHelperTestDigest, []byte("helper")); err == nil {
			t.Errorf("install accepted generation digest %q", digest)
		}
		if _, err := RunSSHBootstrapHelperObserve(context.Background(), config, digest, sshHelperTestDigest, 1); err == nil {
			t.Errorf("observe accepted generation digest %q", digest)
		}
	}
	for _, size := range []int64{0, -1, sshBootstrapHelperMaxBytes + 1} {
		if _, err := RunSSHBootstrapHelperObserve(context.Background(), config, sshHelperTestDigest, sshHelperTestDigest, size); err == nil {
			t.Errorf("observe accepted size %d", size)
		}
	}
}

func newSSHBootstrapHelperTestLayout(t *testing.T) sshBootstrapHelperLayout {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, "opt", "bootstrap-helper")
	launcher := filepath.Join(base, "usr", "local", "libexec", "codex-artifact-install")
	for _, dir := range []string{root, filepath.Dir(launcher)} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return sshBootstrapHelperLayout{root: root, launcher: launcher}
}

func newSSHBootstrapHelperTestConfig(t *testing.T) SSHAppServerConfig {
	t.Helper()
	config := newSSHTestConfig(t)
	config.SSHExecutable = writeSSHBootstrapHelperFake(t, "", "")
	return config
}

func writeSSHBootstrapHelperFake(t *testing.T, marker, beforeExec string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-ssh")
	script := "#!/bin/sh\nset -eu\n"
	if marker != "" {
		script += "printf started > " + shellQuoteForTest(marker) + "\n"
	}
	script += beforeExec + "while [ \"$#\" -gt 0 ] && [ \"$1\" != -- ]; do shift; done\n"
	script += "[ \"$#\" -gt 0 ] || exit 91\nshift\nshift\nexec /bin/sh -c \"$*\"\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeSSHBootstrapHelperArgvFake(t *testing.T, argvPath string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-ssh-argv")
	script := "#!/bin/sh\nset -eu\nprintf '%s\\n' \"$@\" > " + shellQuoteForTest(argvPath) + "\n"
	script += "while [ \"$#\" -gt 0 ] && [ \"$1\" != -- ]; do shift; done\nshift\nshift\nexec /bin/sh -c \"$*\"\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeSSHBootstrapHelperOutputFake(t *testing.T, output string, exit int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-ssh-output")
	script := "#!/bin/sh\nprintf '%s' " + shellQuoteForTest(output) + "\nexit " + fmt.Sprint(exit) + "\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func requireLinuxRoot(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("bootstrap receiver uses Linux filesystem and core utilities")
	}
	if output, err := exec.Command("id", "-u").Output(); err != nil || strings.TrimSpace(string(output)) != "0" {
		t.Skip("bootstrap receiver integration requires root-owned temporary paths")
	}
	if _, err := exec.LookPath("sha256sum"); err != nil {
		t.Skip("sha256sum is unavailable")
	}
}
