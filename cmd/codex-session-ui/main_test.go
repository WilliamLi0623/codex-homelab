package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/WilliamLi0623/codex-homelab/internal/agentd"
	"github.com/WilliamLi0623/codex-homelab/internal/codexsession"
)

func TestLoadSessionConfigRequiresProtectedRoutingInputsAndUsesLoopbackDefault(t *testing.T) {
	values := map[string]string{
		"CODEX_HOME":                   `C:\Users\user\.codex`,
		"CODEX_ROUTING_CONTROLLER_URL": "https://controller.example",
		"CODEX_ROUTING_STATE_TOKEN":    strings.Repeat("s", 40),
		"CODEX_ROUTING_STATE_FILE":     `C:\ProgramData\Codex\routing.json`,
	}
	config, err := loadSessionConfig(func(name string) string { return values[name] }, func(name string) (string, error) {
		if name == "codex" {
			return `C:\Program Files\Codex\codex.exe`, nil
		}
		return "", errors.New("not found")
	})
	if err != nil {
		t.Fatal(err)
	}
	if config.ListenAddress != "127.0.0.1:8765" || config.Origin != "http://127.0.0.1:8765" || config.CodexHome != values["CODEX_HOME"] || config.ControllerToken != values["CODEX_ROUTING_STATE_TOKEN"] {
		t.Fatalf("loaded config=%+v", config)
	}
}

func TestLoadSessionConfigRejectsWildcardListenerAndShortToken(t *testing.T) {
	base := map[string]string{
		"CODEX_HOME": "/home/user/.codex", "CODEX_ROUTING_CONTROLLER_URL": "https://controller.example",
		"CODEX_ROUTING_STATE_TOKEN": strings.Repeat("x", 32), "CODEX_ROUTING_STATE_FILE": "/var/lib/codex/routing.json",
	}
	for name, change := range map[string]func(map[string]string){
		"wildcard":               func(values map[string]string) { values["CODEX_SESSION_UI_LISTEN"] = "0.0.0.0:8765" },
		"short controller token": func(values map[string]string) { values["CODEX_ROUTING_STATE_TOKEN"] = "short" },
		"controller URL query": func(values map[string]string) {
			values["CODEX_ROUTING_CONTROLLER_URL"] = "https://controller.example/?token=secret"
		},
	} {
		t.Run(name, func(t *testing.T) {
			values := make(map[string]string, len(base))
			for key, value := range base {
				values[key] = value
			}
			change(values)
			_, err := loadSessionConfig(func(key string) string { return values[key] }, func(string) (string, error) { return "/usr/bin/codex", nil })
			if err == nil {
				t.Fatal("unsafe config accepted")
			}
			if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "short") {
				t.Fatalf("config error leaked supplied values: %v", err)
			}
		})
	}
}

func TestSafeSessionAppServerEnvironmentExcludesControllerAndProviderKeys(t *testing.T) {
	entries := safeSessionAppServerEnvironment([]string{
		"PATH=/bin", "HOME=/home/user", "OPENAI_API_KEY=do-not-copy", "CCH_API_KEY=do-not-copy",
		"CODEX_ROUTING_STATE_TOKEN=do-not-copy", "HTTPS_PROXY=http://proxy.invalid",
	}, "/home/user/.codex", false)
	joined := strings.Join(entries, "\n")
	for _, secretName := range []string{"OPENAI_API_KEY=", "CCH_API_KEY=", "CODEX_ROUTING_STATE_TOKEN="} {
		if strings.Contains(joined, secretName) {
			t.Fatalf("secret environment variable was propagated: %s", secretName)
		}
	}
	if !strings.Contains(joined, "CODEX_HOME=/home/user/.codex") || !strings.Contains(joined, "HTTPS_PROXY=http://proxy.invalid") {
		t.Fatalf("safe runtime environment lost required settings: %q", joined)
	}
}

func TestSessionUIExtendedAppServerClientUsesProcessOwnedStdio(t *testing.T) {
	process, err := agentd.StartIsolatedProcess(context.Background(), os.Args[0], []string{"-test.run=TestSessionUIAppServerHelperProcess"}, append(safeSessionAppServerEnvironment(os.Environ(), t.TempDir(), runtime.GOOS == "windows"), "GO_WANT_SESSION_UI_HELPER=1"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = process.Close() })
	reader, writer := process.AppServerStdio()
	client := codexsession.NewAppServerClient(codexsession.NewProtocolClient(reader, writer))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := client.Initialize(ctx); err != nil {
		t.Fatalf("extended App Server client could not initialize over process-owned stdio: %v", err)
	}
}

func TestSessionUIAppServerHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_SESSION_UI_HELPER") != "1" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var request struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
		}
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			continue
		}
		switch request.Method {
		case "initialize":
			encoded, _ := json.Marshal(map[string]any{"id": request.ID, "result": map[string]any{}})
			_, _ = fmt.Fprintln(os.Stdout, string(encoded))
		case "initialized":
			return
		}
	}
	if err := scanner.Err(); err != nil {
		os.Exit(2)
	}
}
