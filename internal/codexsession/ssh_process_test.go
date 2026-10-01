package codexsession

import (
	"context"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestBuildSSHAppServerCommandUsesPinnedHostAndFixedRemoteCommand(t *testing.T) {
	config := newSSHTestConfig(t)
	executable, args, environment, err := BuildSSHAppServerCommand(config, []string{
		"PATH=/usr/bin:/bin",
		"LANG=en_US.UTF-8",
		"OPENAI_API_KEY=must-not-leak",
		"CCH_API_KEY=must-not-leak-either",
		"CODEX_HOME=/local/codex-home",
		"SSH_AUTH_SOCK=/tmp/agent.sock",
		"SSH_AGENT_PID=1234",
		"HTTPS_PROXY=http://proxy.invalid",
	})
	if err != nil {
		t.Fatal(err)
	}
	if executable != config.SSHExecutable {
		t.Fatalf("SSH executable = %q, want %q", executable, config.SSHExecutable)
	}
	joined := strings.Join(args, "\n")
	for _, option := range []string{
		"-F\n/dev/null", "-T", "BatchMode=yes", "StrictHostKeyChecking=yes",
		"IdentitiesOnly=yes", "IdentityAgent=none", "GlobalKnownHostsFile=/dev/null",
		"UserKnownHostsFile=" + config.KnownHostsFile,
		"HostKeyAlias=" + config.HostKeyAlias,
		"ForwardAgent=no", "ClearAllForwardings=yes",
		"PasswordAuthentication=no", "KbdInteractiveAuthentication=no",
		"ConnectTimeout=10", "ServerAliveInterval=15", "ServerAliveCountMax=3",
		"root@192.0.2.44", "/usr/bin/env", "-i", "HOME=/root",
		"CODEX_HOME=/srv/codex/session-1", "PATH=/usr/local/bin:/usr/bin:/bin",
		"LANG=C.UTF-8", "/usr/local/bin/codex", "app-server", "--listen", "stdio://",
	} {
		if !strings.Contains(joined, option) {
			t.Errorf("SSH arguments omit required argument %q: %q", option, args)
		}
	}
	if strings.Contains(joined, "must-not-leak") || strings.Contains(joined, "proxy.invalid") {
		t.Fatalf("SSH arguments contain an inherited secret: %q", args)
	}
	identityFlag := indexOfSSHArg(args, "-i")
	if identityFlag < 0 || identityFlag+1 >= len(args) || args[identityFlag+1] != config.IdentityFile {
		t.Fatalf("SSH identity selection = %q, want %q immediately after -i", args, config.IdentityFile)
	}
	commandIndex := indexOfSSHArg(args, "/usr/bin/env")
	if commandIndex < 0 {
		t.Fatalf("SSH arguments omit the fixed remote command: %q", args)
	}
	wantRemote := []string{
		"/usr/bin/env", "-i", "HOME=/root", "CODEX_HOME=/srv/codex/session-1",
		"PATH=/usr/local/bin:/usr/bin:/bin", "LANG=C.UTF-8",
		"/usr/local/bin/codex", "app-server", "--listen", "stdio://",
	}
	if strings.Join(args[commandIndex:], "\n") != strings.Join(wantRemote, "\n") {
		t.Fatalf("remote command = %q, want fixed command %q", args[commandIndex:], wantRemote)
	}
	if len(environment) != 2 || environment[0] != "PATH=/usr/bin:/bin" || environment[1] != "LANG=en_US.UTF-8" {
		t.Fatalf("local SSH environment = %#v, want only the supplied PATH and LANG", environment)
	}
}

func TestBuildSSHAppServerCommandRejectsUnsafeTargetAndHomeValues(t *testing.T) {
	base := newSSHTestConfig(t)
	for _, tc := range []struct {
		name   string
		change func(*SSHAppServerConfig)
	}{
		{"dns name", func(c *SSHAppServerConfig) { c.Address = "server.example" }},
		{"ssh option injection", func(c *SSHAppServerConfig) { c.Address = "-oProxyCommand=evil" }},
		{"shell metacharacters", func(c *SSHAppServerConfig) { c.Address = "192.0.2.44;touch /tmp/pwned" }},
		{"multicast address", func(c *SSHAppServerConfig) { c.Address = "224.0.0.1" }},
		{"broadcast address", func(c *SSHAppServerConfig) { c.Address = "255.255.255.255" }},
		{"zone scoped ipv6", func(c *SSHAppServerConfig) { c.Address = "fe80::1%eth0" }},
		{"bad alias", func(c *SSHAppServerConfig) { c.HostKeyAlias = "p28-session-abc;echo" }},
		{"short alias", func(c *SSHAppServerConfig) { c.HostKeyAlias = "p28-session-a" }},
		{"home traversal", func(c *SSHAppServerConfig) { c.CodexHome = "/srv/codex/../root" }},
		{"home shell text", func(c *SSHAppServerConfig) { c.CodexHome = "/srv/codex;touch /tmp/pwned" }},
		{"filesystem root", func(c *SSHAppServerConfig) { c.CodexHome = "/" }},
		{"root itself", func(c *SSHAppServerConfig) { c.CodexHome = "/root" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := base
			tc.change(&config)
			if _, _, _, err := BuildSSHAppServerCommand(config, nil); err == nil {
				t.Fatal("unsafe SSH configuration was accepted")
			}
		})
	}
}

func TestBuildSSHAppServerCommandAcceptsIPv6WithoutAmbiguousPortSyntax(t *testing.T) {
	config := newSSHTestConfig(t)
	config.Address = "2001:db8::44"
	_, args, _, err := BuildSSHAppServerCommand(config, nil)
	if err != nil {
		t.Fatal(err)
	}
	commandIndex := indexOfSSHArg(args, "/usr/bin/env")
	if commandIndex < 1 || args[commandIndex-1] != "root@2001:db8::44" {
		t.Fatalf("IPv6 SSH destination before fixed remote command is missing: %q", args)
	}
}

func TestBuildSSHAppServerCommandRejectsMissingSymlinkedAndInsecurePinMaterial(t *testing.T) {
	config := newSSHTestConfig(t)
	missing := config
	missing.IdentityFile = filepath.Join(t.TempDir(), "missing-key")
	if _, _, _, err := BuildSSHAppServerCommand(missing, nil); err == nil {
		t.Fatal("missing identity file was accepted")
	}

	if runtime.GOOS == "windows" {
		t.Skip("POSIX mode and symlink material checks run on POSIX")
	}
	for _, tc := range []struct {
		name string
		edit func(*testing.T, *SSHAppServerConfig)
	}{
		{"symlink identity", func(t *testing.T, c *SSHAppServerConfig) {
			link := filepath.Join(t.TempDir(), "identity-link")
			if err := os.Symlink(c.IdentityFile, link); err != nil {
				t.Fatal(err)
			}
			c.IdentityFile = link
		}},
		{"symlink executable", func(t *testing.T, c *SSHAppServerConfig) {
			link := filepath.Join(t.TempDir(), "ssh-link")
			if err := os.Symlink(c.SSHExecutable, link); err != nil {
				t.Fatal(err)
			}
			c.SSHExecutable = link
		}},
		{"insecure identity mode", func(t *testing.T, c *SSHAppServerConfig) {
			if err := os.Chmod(c.IdentityFile, 0o640); err != nil {
				t.Fatal(err)
			}
		}},
		{"symlink known hosts", func(t *testing.T, c *SSHAppServerConfig) {
			link := filepath.Join(t.TempDir(), "known-hosts-link")
			if err := os.Symlink(c.KnownHostsFile, link); err != nil {
				t.Fatal(err)
			}
			c.KnownHostsFile = link
		}},
		{"insecure known hosts mode", func(t *testing.T, c *SSHAppServerConfig) {
			if err := os.Chmod(c.KnownHostsFile, 0o644); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := newSSHTestConfig(t)
			tc.edit(t, &config)
			if _, _, _, err := BuildSSHAppServerCommand(config, nil); err == nil {
				t.Fatal("unsafe pin or identity material was accepted")
			}
		})
	}
}

func TestBuildSSHAppServerCommandRejectsSymlinkedParentDirectories(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX parent traversal check")
	}
	config := newSSHTestConfig(t)
	root := t.TempDir()
	target := filepath.Join(root, "real")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	identity := filepath.Join(target, "identity")
	if err := os.WriteFile(identity, []byte("not-read-by-transport"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "linked")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	config.IdentityFile = filepath.Join(link, "identity")
	if _, _, _, err := BuildSSHAppServerCommand(config, nil); err == nil {
		t.Fatal("identity path through a symlinked parent was accepted")
	}
}

func TestSSHPathModeRejectsWritableAncestorExceptTrustedStickyTemp(t *testing.T) {
	if err := validateSSHPathMode(os.ModeDir|0o777, sshPathDirectoryAncestor, false); err == nil {
		t.Fatal("group/world-writable non-sticky ancestor was accepted")
	}
	if err := validateSSHPathMode(os.ModeDir|os.ModeSticky|0o1777, sshPathDirectoryAncestor, false); err == nil {
		t.Fatal("sticky writable directory outside an approved temp path was accepted")
	}
	if err := validateSSHPathMode(os.ModeDir|os.ModeSticky|0o1777, sshPathDirectoryAncestor, true); err != nil {
		t.Fatalf("trusted sticky temp directory was rejected: %v", err)
	}
}

func TestSSHPathModeRejectsGroupOrWorldWritableExecutable(t *testing.T) {
	for _, mode := range []os.FileMode{0o777, 0o775, 0o757} {
		if err := validateSSHPathMode(mode, sshPathExecutable, false); err == nil {
			t.Errorf("group/world-writable executable mode %04o was accepted", mode.Perm())
		}
	}
	if err := validateSSHPathMode(0o755, sshPathExecutable, false); err != nil {
		t.Fatalf("read/execute access without group/other write was rejected: %v", err)
	}
}

func TestStartSSHAppServerProcessUsesSanitizedEnvironmentAndClosesOnEOF(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("fake OpenSSH subprocess lifecycle harness requires Linux")
	}
	config := newSSHTestConfig(t)
	dir := t.TempDir()
	sshPath := filepath.Join(dir, "fake-ssh")
	argvPath := filepath.Join(dir, "argv")
	envPath := filepath.Join(dir, "environment")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$@\" > " + shellQuoteForTest(argvPath) + "\n" +
		"/usr/bin/env > " + shellQuoteForTest(envPath) + "\n" +
		"/bin/cat >/dev/null\n"
	if err := os.WriteFile(sshPath, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	config.SSHExecutable = sshPath
	t.Setenv("OPENAI_API_KEY", "secret-provider-value")
	t.Setenv("CCH_API_KEY", "secret-cch-value")
	t.Setenv("SSH_AUTH_SOCK", "/tmp/secret-agent.sock")
	t.Setenv("HTTPS_PROXY", "http://secret-proxy.invalid")
	process, err := StartSSHAppServerProcess(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	closeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := process.Close(closeCtx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-process.Done():
	default:
		t.Fatal("SSH-backed App Server did not finish after stdin EOF")
	}
	argv, err := os.ReadFile(argvPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(argv), "StrictHostKeyChecking=yes") || !strings.Contains(string(argv), "UserKnownHostsFile="+config.KnownHostsFile) {
		t.Fatalf("fake SSH received no strict explicit host pin: %s", argv)
	}
	environment, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"secret-provider-value", "secret-cch-value", "secret-agent.sock", "secret-proxy.invalid", "CODEX_HOME="} {
		if strings.Contains(string(environment), secret) {
			t.Errorf("local SSH environment contains %q", secret)
		}
	}
}

func TestStartSSHAppServerProcessRejectsUnsupportedHost(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("non-Linux host rejection")
	}
	process, err := StartSSHAppServerProcess(context.Background(), newSSHTestConfig(t))
	if err == nil || process != nil || !strings.Contains(err.Error(), "Linux Controller") {
		t.Fatalf("unsupported host launch: process=%v err=%v", process, err)
	}
}

func newSSHTestConfig(t *testing.T) SSHAppServerConfig {
	t.Helper()
	dir := t.TempDir()
	ssh := filepath.Join(dir, "ssh")
	identity := filepath.Join(dir, "identity")
	knownHosts := filepath.Join(dir, "known-hosts")
	for _, file := range []string{ssh, identity, knownHosts} {
		if err := os.WriteFile(file, []byte("fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(ssh, 0o700); err != nil {
		t.Fatal(err)
	}
	return SSHAppServerConfig{
		SSHExecutable:  ssh,
		Address:        netip.MustParseAddr("192.0.2.44").String(),
		IdentityFile:   identity,
		KnownHostsFile: knownHosts,
		HostKeyAlias:   "p28-session-0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		CodexHome:      "/srv/codex/session-1",
	}
}

func shellQuoteForTest(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func indexOfSSHArg(args []string, want string) int {
	for i, arg := range args {
		if arg == want {
			return i
		}
	}
	return -1
}
