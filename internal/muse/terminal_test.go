package muse

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestTerminalRunsInWorkspaceAndReturnsOutput(t *testing.T) {
	workspace := t.TempDir()
	terminal := NewTerminal(workspace)
	result, err := terminal.Run(context.Background(), markerCommand())
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 0 || result.Stdout != "" {
		t.Fatalf("result = %+v", result)
	}
	data, err := os.ReadFile(filepath.Join(workspace, "marker.txt"))
	if err != nil || string(data) != "P13-E2E" {
		t.Fatalf("marker = %q, err=%v", data, err)
	}
}

func TestLimitedBufferCapsOutput(t *testing.T) {
	b := &limitedBuffer{limit: 8}
	if _, err := b.Write([]byte("123456789")); err != nil {
		t.Fatal(err)
	}
	if b.String() != "12345678" || !b.truncated {
		t.Fatalf("buffer=%q truncated=%v", b.String(), b.truncated)
	}
}

func TestTerminalRejectsWorkspaceEscapeAndSecretAccess(t *testing.T) {
	terminal := NewTerminal(t.TempDir())
	for _, command := range []string{"cd /tmp && pwd", "cat /etc/passwd", "env CODEX_API_KEY=leak printf x", "ssh host true"} {
		if _, err := terminal.Run(context.Background(), command); err == nil || !strings.Contains(err.Error(), "rejected") {
			t.Fatalf("command %q error = %v; want rejection", command, err)
		}
	}
}

func TestTerminalEnforcesTimeoutOutputAndCommandLimit(t *testing.T) {
	terminal := NewTerminalWithLimits(t.TempDir(), 20*time.Millisecond, 8, 1)
	if _, err := terminal.Run(context.Background(), timeoutCommand()); err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("timeout error = %v", err)
	}
	terminal = NewTerminalWithLimits(t.TempDir(), 5*time.Second, 8, 2)
	result, err := terminal.Run(context.Background(), outputCommand())
	if err != nil || !result.Truncated || len(result.Stdout) > 8 {
		t.Fatalf("output result=%+v err=%v", result, err)
	}
	if _, err := terminal.Run(context.Background(), shortOutputCommand()); err != nil {
		t.Fatal(err)
	}
	if _, err := terminal.Run(context.Background(), "printf too-many"); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("limit error = %v", err)
	}
}

func markerCommand() string {
	if runtime.GOOS == "windows" {
		return "Set-Content -NoNewline -Path marker.txt -Value P13-E2E"
	}
	return "printf P13-E2E > marker.txt"
}

func timeoutCommand() string {
	if runtime.GOOS == "windows" {
		return "Start-Sleep -Seconds 1"
	}
	return "sleep 1"
}

func outputCommand() string {
	if runtime.GOOS == "windows" {
		return "Write-Output 123456789"
	}
	return "printf 123456789"
}

func shortOutputCommand() string {
	if runtime.GOOS == "windows" {
		return "Write-Output ok"
	}
	return "printf ok"
}
