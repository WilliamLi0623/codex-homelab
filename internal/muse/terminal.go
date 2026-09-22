package muse

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

type ToolResult struct {
	ExitCode  int    `json:"exit_code"`
	Stdout    string `json:"stdout"`
	Stderr    string `json:"stderr"`
	Truncated bool   `json:"truncated"`
}

type Terminal struct {
	Workspace      string
	Timeout        time.Duration
	MaxOutputBytes int
	MaxCommands    int
	mu             sync.Mutex
	commands       int
}

func NewTerminal(workspace string) *Terminal {
	return NewTerminalWithLimits(workspace, 2*time.Minute, 64*1024, 32)
}

func NewTerminalWithLimits(workspace string, timeout time.Duration, maxOutputBytes, maxCommands int) *Terminal {
	return &Terminal{Workspace: workspace, Timeout: timeout, MaxOutputBytes: maxOutputBytes, MaxCommands: maxCommands}
}

func (t *Terminal) Run(parent context.Context, command string) (ToolResult, error) {
	if t == nil || strings.TrimSpace(t.Workspace) == "" {
		return ToolResult{}, errors.New("terminal workspace is required")
	}
	if !isSafeTerminalCommand(command) {
		return ToolResult{}, fmt.Errorf("terminal command rejected: unsafe command")
	}
	t.mu.Lock()
	if t.MaxCommands > 0 && t.commands >= t.MaxCommands {
		t.mu.Unlock()
		return ToolResult{}, errors.New("terminal command limit reached")
	}
	t.commands++
	t.mu.Unlock()

	ctx := parent
	cancel := func() {}
	if t.Timeout > 0 {
		ctx, cancel = context.WithTimeout(parent, t.Timeout)
	}
	defer cancel()
	cmd := terminalCommand(ctx, command)
	cmd.Dir = t.Workspace
	cmd.Env = safeTerminalEnvironment(t.Workspace)
	stdout := &limitedBuffer{limit: t.MaxOutputBytes}
	stderr := &limitedBuffer{limit: t.MaxOutputBytes}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err := cmd.Run()
	result := ToolResult{ExitCode: 0, Stdout: stdout.String(), Stderr: stderr.String(), Truncated: stdout.truncated || stderr.truncated}
	if cmd.ProcessState != nil && cmd.ProcessState.ExitCode() >= 0 {
		result.ExitCode = cmd.ProcessState.ExitCode()
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return result, fmt.Errorf("terminal command timed out: %w", ctx.Err())
	}
	if err != nil {
		return result, fmt.Errorf("terminal command failed with exit code %d: %w", result.ExitCode, err)
	}
	return result, nil
}

func terminalCommand(ctx context.Context, command string) *exec.Cmd {
	if runtime.GOOS == "windows" {
		return exec.CommandContext(ctx, "powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", command)
	}
	return exec.CommandContext(ctx, "/bin/sh", "-c", command)
}

func isSafeTerminalCommand(command string) bool {
	lower := strings.ToLower(strings.TrimSpace(command))
	if lower == "" {
		return false
	}
	for _, denied := range []string{
		"../", "..\\", "cd /", "cd ~/", " /etc", " /root", " /proc", " /sys", " /dev",
		"set-location c:\\", "get-content c:\\windows", "$env:codex_api_key", "$env:openai_api_key",
		"ssh ", "scp ", "rsync ", "curl ", "wget ", " nc ", "kubectl ", "proxmox", "pct ",
		"git push", "git remote add", "printenv", " env ", "export ", "codex_api_key", "openai_api_key",
	} {
		if strings.Contains(lower, denied) {
			return false
		}
	}
	return true
}

func safeTerminalEnvironment(workspace string) []string {
	if runtime.GOOS == "windows" {
		values := []string{"Path=" + os.Getenv("Path"), "SystemRoot=" + os.Getenv("SystemRoot"), "TEMP=" + os.Getenv("TEMP"), "TMP=" + os.Getenv("TMP"), "USERPROFILE=" + workspace, "CODEX_WORKSPACE=" + workspace}
		return appendGitIdentity(values)
	}
	values := []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=" + workspace, "LANG=C", "CODEX_WORKSPACE=" + workspace}
	return appendGitIdentity(values)
}

func appendGitIdentity(values []string) []string {
	for _, entry := range os.Environ() {
		name, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		switch name {
		case "GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL":
			values = append(values, entry)
		}
	}
	return values
}

type limitedBuffer struct {
	bytes.Buffer
	limit     int
	truncated bool
}

func (b *limitedBuffer) Write(data []byte) (int, error) {
	if b.limit <= 0 {
		b.truncated = len(data) > 0
		return len(data), nil
	}
	remaining := b.limit - b.Len()
	if remaining <= 0 {
		b.truncated = len(data) > 0
		return len(data), nil
	}
	if len(data) > remaining {
		_, _ = b.Buffer.Write(data[:remaining])
		b.truncated = true
		return len(data), nil
	}
	return b.Buffer.Write(data)
}

func (b *limitedBuffer) ReadFrom(reader io.Reader) (int64, error) {
	chunk := make([]byte, 32*1024)
	var total int64
	for {
		n, err := reader.Read(chunk)
		if n > 0 {
			_, _ = b.Write(chunk[:n])
			total += int64(n)
		}
		if errors.Is(err, io.EOF) {
			return total, nil
		}
		if err != nil {
			return total, err
		}
	}
}

var _ io.Writer = (*limitedBuffer)(nil)
var _ io.ReaderFrom = (*limitedBuffer)(nil)
