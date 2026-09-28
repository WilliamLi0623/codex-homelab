package codexsession

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBuildSafeAppServerEnvironmentRequiresHomeAndExcludesSecrets(t *testing.T) {
	home, err := filepath.Abs(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	input := []string{
		"PATH=C:/Windows/System32",
		"USERPROFILE=C:/Users/test",
		"SYSTEMROOT=C:/Windows",
		"HTTP_PROXY=http://proxy.invalid:8080",
		"OPENAI_API_KEY=do-not-copy",
		"CCH_API_KEY=do-not-copy-either",
	}
	env, err := BuildSafeAppServerEnvironment(input, home)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(env, "\n")
	if !strings.Contains(joined, "CODEX_HOME="+home) || !strings.Contains(joined, "PATH=C:/Windows/System32") || !strings.Contains(joined, "HTTP_PROXY=http://proxy.invalid:8080") {
		t.Fatalf("required runtime environment was not retained: %q", joined)
	}
	if strings.Contains(joined, "OPENAI_API_KEY") || strings.Contains(joined, "CCH_API_KEY") {
		t.Fatalf("provider secrets were inherited: %q", joined)
	}
	if _, err := BuildSafeAppServerEnvironment(input, "relative-home"); err == nil {
		t.Fatal("relative CODEX_HOME was accepted")
	}
}

func TestAppServerProcessClosesGracefullyOnStdinEOF(t *testing.T) {
	if os.Getenv("CODEX_SESSION_PROCESS_HELPER") == "1" {
		buffer := make([]byte, 1)
		for {
			if _, err := os.Stdin.Read(buffer); err != nil {
				return
			}
		}
	}

	ctx := context.Background()
	env := []string{"CODEX_SESSION_PROCESS_HELPER=1"}
	process, err := startAppServerCommand(ctx, os.Args[0], []string{"-test.run=^TestAppServerProcessClosesGracefullyOnStdinEOF$"}, env)
	if err != nil {
		t.Fatal(err)
	}
	closeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := process.Close(closeCtx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-process.Done():
	default:
		t.Fatal("process did not exit after stdin EOF")
	}
}
