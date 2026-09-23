package main

import (
	"context"
	"errors"
	"log"
	"net"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/WilliamLi0623/codex-homelab/internal/codexrouting"
)

func main() {
	if err := run(); err != nil && !errors.Is(err, context.Canceled) {
		log.Printf("codex routing agent stopped: %v", err)
		os.Exit(1)
	}
}

func run() error {
	controllerURL, err := controllerRoutingStateURL(os.Getenv("CODEX_ROUTING_CONTROLLER_URL"))
	if err != nil {
		return err
	}
	token := os.Getenv("CODEX_ROUTING_STATE_TOKEN")
	if len(token) < 32 {
		return errors.New("CODEX_ROUTING_STATE_TOKEN must contain at least 32 characters")
	}
	codexHome := strings.TrimSpace(os.Getenv("CODEX_HOME"))
	if codexHome == "" {
		return errors.New("CODEX_HOME is required for an isolated quota App Server")
	}
	stateFile := strings.TrimSpace(os.Getenv("CODEX_ROUTING_STATE_FILE"))
	if stateFile == "" {
		return errors.New("CODEX_ROUTING_STATE_FILE is required")
	}
	interval := 30 * time.Second
	if raw := strings.TrimSpace(os.Getenv("CODEX_ROUTING_POLL_INTERVAL_SECONDS")); raw != "" {
		seconds, err := strconv.Atoi(raw)
		if err != nil || seconds < 5 || seconds > 3600 {
			return errors.New("CODEX_ROUTING_POLL_INTERVAL_SECONDS must be between 5 and 3600")
		}
		interval = time.Duration(seconds) * time.Second
	}
	executable := strings.TrimSpace(os.Getenv("CODEX_EXECUTABLE"))
	if executable == "" {
		executable, err = exec.LookPath("codex")
		if err != nil {
			return errors.New("CODEX_EXECUTABLE is required when codex is not on PATH")
		}
	}
	quotaTransport := codexrouting.CodexAppServerTransport{
		Executable:  executable,
		Environment: append(safeAppServerEnvironment(), "CODEX_HOME="+codexHome),
	}
	reader := codexrouting.AppServerQuotaClient{Transport: quotaTransport}
	sink := codexrouting.HTTPModeSink{URL: controllerURL, Token: token}
	stateStore := &codexrouting.FileRoutingStateStore{Path: filepath.Clean(stateFile)}
	coordinator, err := codexrouting.NewCoordinator(reader, sink, stateStore, interval)
	if err != nil {
		return err
	}
	coordinator.OnError = func(err error) { log.Printf("quota routing poll failed: %v", err) }
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return coordinator.Run(ctx)
}

func controllerRoutingStateURL(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("CODEX_ROUTING_CONTROLLER_URL is required")
	}
	u, err := url.Parse(value)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("CODEX_ROUTING_CONTROLLER_URL must be an HTTP(S) controller URL without credentials, query, or fragment")
	}
	if u.Scheme == "http" {
		hostname := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
		if hostname != "localhost" {
			ip := net.ParseIP(hostname)
			if ip == nil || !ip.IsLoopback() {
				return "", errors.New("CODEX_ROUTING_CONTROLLER_URL must use HTTPS unless the host is loopback or localhost")
			}
		}
	}
	if !strings.HasSuffix(strings.TrimRight(u.Path, "/"), "/internal/v1/routing-state") {
		u.Path = strings.TrimRight(u.Path, "/") + "/internal/v1/routing-state"
	}
	return u.String(), nil
}

func safeAppServerEnvironment() []string {
	allow := map[string]bool{"PATH": true, "HOME": true, "USER": true, "TMPDIR": true, "TEMP": true, "TMP": true, "LANG": true}
	if runtime.GOOS == "windows" {
		for _, key := range []string{"SYSTEMROOT", "WINDIR", "USERPROFILE", "APPDATA", "LOCALAPPDATA"} {
			allow[key] = true
		}
	}
	result := make([]string, 0, len(allow)+1)
	for _, entry := range os.Environ() {
		key, _, ok := strings.Cut(entry, "=")
		if ok && allow[strings.ToUpper(key)] {
			result = append(result, entry)
		}
	}
	return result
}
