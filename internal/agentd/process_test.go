package agentd

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func TestStartProcessConnectsClientOverStdio(t *testing.T) {
	if os.Getenv("GO_WANT_AGENTD_HELPER") == "1" {
		TestAgentdHelperProcess(t)
		return
	}
	process, err := StartProcess(context.Background(), os.Args[0], []string{"-test.run=TestStartProcessConnectsClientOverStdio", "--", "app-server"}, []string{"GO_WANT_AGENTD_HELPER=1"})
	if err != nil {
		t.Fatalf("StartProcess() error = %v", err)
	}
	t.Cleanup(func() { _ = process.Close() })
	if err := process.Client.Initialize(context.Background()); err != nil {
		t.Fatalf("Client.Initialize() error = %v", err)
	}
}

func TestAgentdHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_AGENTD_HELPER") != "1" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.Contains(line, `"method":"initialize"`) {
			fmt.Fprintln(os.Stdout, `{"id":1,"result":{}}`)
			continue
		}
		if strings.Contains(line, `"method":"initialized"`) {
			return
		}
	}
	os.Exit(1)
}

func TestProcessCloseStopsChild(t *testing.T) {
	if os.Getenv("GO_WANT_AGENTD_SLEEP_HELPER") == "1" {
		select {}
	}
	process, err := StartProcess(context.Background(), os.Args[0], []string{"-test.run=TestProcessCloseStopsChild", "--", "app-server"}, []string{"GO_WANT_AGENTD_SLEEP_HELPER=1"})
	if err != nil {
		t.Fatalf("StartProcess() error = %v", err)
	}
	if err := process.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	select {
	case <-process.Done():
	case <-time.After(time.Second):
		t.Fatal("child did not exit after Close")
	}
}

func TestStartCodexAppServerUsesStdioProtocol(t *testing.T) {
	if os.Getenv("GO_WANT_AGENTD_HELPER") == "1" {
		TestAgentdHelperProcess(t)
		return
	}
	process, err := StartCodexAppServer(context.Background(), os.Args[0], []string{"GO_WANT_AGENTD_HELPER=1", "CODEX_HOME=test-attempt-home"})
	if err != nil {
		t.Fatalf("StartCodexAppServer() error = %v", err)
	}
	t.Cleanup(func() { _ = process.Close() })
	if err := process.Client.Initialize(context.Background()); err != nil {
		t.Fatalf("Client.Initialize() error = %v", err)
	}
}

func TestStartCodexAppServerRequiresExplicitCodexHome(t *testing.T) {
	_, err := StartCodexAppServer(context.Background(), os.Args[0], nil)
	if err == nil || !strings.Contains(err.Error(), "CODEX_HOME") {
		t.Fatalf("StartCodexAppServer() error = %v, want explicit CODEX_HOME rejection", err)
	}
}

func TestRealCodexAppServerInitializesWhenExplicitlyEnabled(t *testing.T) {
	if os.Getenv("CODEX_AGENTD_REAL") != "1" {
		t.Skip("set CODEX_AGENTD_REAL=1 to probe the installed Codex App Server")
	}
	executable := os.Getenv("CODEX_AGENTD_BINARY")
	if executable == "" {
		t.Fatal("CODEX_AGENTD_BINARY is required when real probe is enabled")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	process, err := StartCodexAppServer(ctx, executable, []string{"CODEX_HOME=C:\\Users\\William Li\\Documents\\Codex\\2026-09-19\\g\\work\\agentd-real-codex-home"})
	if err != nil {
		t.Fatalf("StartCodexAppServer() error = %v", err)
	}
	defer func() { _ = process.Close() }()
	if err := process.Client.Initialize(ctx); err != nil {
		t.Fatalf("real Client.Initialize() error = %v", err)
	}
}
