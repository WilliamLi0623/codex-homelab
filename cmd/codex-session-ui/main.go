package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
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

	"github.com/WilliamLi0623/codex-homelab/internal/agentd"
	"github.com/WilliamLi0623/codex-homelab/internal/codexrouting"
	"github.com/WilliamLi0623/codex-homelab/internal/codexsession"
)

const defaultSessionListenAddress = "127.0.0.1:8765"

type serviceConfig struct {
	ListenAddress   string
	Origin          string
	CodexHome       string
	CodexExecutable string
	ControllerURL   string
	ControllerToken string
	StateFile       string
	PollInterval    time.Duration
	StaticDirectory string
}

func main() {
	if err := run(); err != nil && !errors.Is(err, context.Canceled) {
		log.Printf("codex session UI stopped: %v", err)
		os.Exit(1)
	}
}

func loadSessionConfig(getenv func(string) string, lookPath func(string) (string, error)) (serviceConfig, error) {
	listen := strings.TrimSpace(getenv("CODEX_SESSION_UI_LISTEN"))
	if listen == "" {
		listen = defaultSessionListenAddress
	}
	host, portText, err := net.SplitHostPort(listen)
	if err != nil || host != "127.0.0.1" {
		return serviceConfig{}, errors.New("CODEX_SESSION_UI_LISTEN must bind to 127.0.0.1")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return serviceConfig{}, errors.New("CODEX_SESSION_UI_LISTEN has an invalid port")
	}
	controllerURL, err := sessionControllerStateURL(getenv("CODEX_ROUTING_CONTROLLER_URL"))
	if err != nil {
		return serviceConfig{}, err
	}
	controllerToken := strings.TrimSpace(getenv("CODEX_ROUTING_STATE_TOKEN"))
	if len(controllerToken) < 32 {
		return serviceConfig{}, errors.New("CODEX_ROUTING_STATE_TOKEN must contain at least 32 characters")
	}
	codexHome := strings.TrimSpace(getenv("CODEX_HOME"))
	if codexHome == "" {
		return serviceConfig{}, errors.New("CODEX_HOME is required")
	}
	stateFile := strings.TrimSpace(getenv("CODEX_ROUTING_STATE_FILE"))
	if stateFile == "" {
		return serviceConfig{}, errors.New("CODEX_ROUTING_STATE_FILE is required")
	}
	executable := strings.TrimSpace(getenv("CODEX_EXECUTABLE"))
	if executable == "" {
		executable, err = lookPath("codex")
		if err != nil {
			return serviceConfig{}, errors.New("CODEX_EXECUTABLE is required when codex is not on PATH")
		}
	}
	interval := 30 * time.Second
	if raw := strings.TrimSpace(getenv("CODEX_ROUTING_POLL_INTERVAL_SECONDS")); raw != "" {
		seconds, parseErr := strconv.Atoi(raw)
		if parseErr != nil || seconds < 5 || seconds > 3600 {
			return serviceConfig{}, errors.New("CODEX_ROUTING_POLL_INTERVAL_SECONDS must be between 5 and 3600")
		}
		interval = time.Duration(seconds) * time.Second
	}
	return serviceConfig{
		ListenAddress: listen, Origin: "http://" + net.JoinHostPort(host, portText),
		CodexHome: codexHome, CodexExecutable: executable,
		ControllerURL: controllerURL, ControllerToken: controllerToken,
		StateFile: filepath.Clean(stateFile), PollInterval: interval,
		StaticDirectory: strings.TrimSpace(getenv("CODEX_SESSION_UI_STATIC_DIR")),
	}, nil
}

func sessionControllerStateURL(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("CODEX_ROUTING_CONTROLLER_URL is required")
	}
	u, err := url.Parse(value)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("CODEX_ROUTING_CONTROLLER_URL must be an HTTP(S) URL without credentials, query, or fragment")
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

func safeSessionAppServerEnvironment(parent []string, codexHome string, windows bool) []string {
	allow := map[string]bool{
		"PATH": true, "HOME": true, "USER": true, "TMPDIR": true, "TEMP": true, "TMP": true,
		"LANG": true, "LC_ALL": true, "HTTP_PROXY": true, "HTTPS_PROXY": true, "ALL_PROXY": true, "NO_PROXY": true,
	}
	if windows {
		for _, key := range []string{"SYSTEMROOT", "WINDIR", "USERPROFILE", "APPDATA", "LOCALAPPDATA"} {
			allow[key] = true
		}
	}
	result := make([]string, 0, len(allow)+1)
	for _, entry := range parent {
		key, _, ok := strings.Cut(entry, "=")
		if ok && allow[strings.ToUpper(key)] {
			result = append(result, entry)
		}
	}
	return append(result, "CODEX_HOME="+codexHome)
}

func newLocalToken() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("create local session token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func run() error {
	config, err := loadSessionConfig(os.Getenv, exec.LookPath)
	if err != nil {
		return err
	}
	if err := requireDirectory(config.CodexHome); err != nil {
		return errors.New("CODEX_HOME must be an existing directory")
	}
	var staticFiles fs.FS
	if config.StaticDirectory != "" {
		if err := requireDirectory(config.StaticDirectory); err != nil {
			return errors.New("CODEX_SESSION_UI_STATIC_DIR must be an existing directory")
		}
		staticFiles = os.DirFS(config.StaticDirectory)
	}
	localToken, err := newLocalToken()
	if err != nil {
		return err
	}

	quotaTransport := codexrouting.CodexAppServerTransport{
		Executable:  config.CodexExecutable,
		Environment: safeSessionAppServerEnvironment(os.Environ(), config.CodexHome, runtime.GOOS == "windows"),
	}
	reader := codexrouting.AppServerQuotaClient{Transport: quotaTransport}
	sink := codexrouting.HTTPModeSink{URL: config.ControllerURL, Token: config.ControllerToken}
	store := &codexrouting.FileRoutingStateStore{Path: config.StateFile}
	coordinator, err := codexrouting.NewCoordinator(reader, sink, store, config.PollInterval)
	if err != nil {
		return err
	}
	coordinator.OnError = func(error) { log.Print("quota routing update failed; last published route is retained") }

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	appServer, err := agentd.StartIsolatedCodexAppServer(ctx, config.CodexExecutable, safeSessionAppServerEnvironment(os.Environ(), config.CodexHome, runtime.GOOS == "windows"))
	if err != nil {
		return errors.New("Codex App Server process could not start")
	}
	defer func() { _ = appServer.Close() }()
	appReader, appWriter := appServer.AppServerStdio()
	sessionClient := codexsession.NewAppServerClient(codexsession.NewProtocolClient(appReader, appWriter))
	sessions, err := codexsession.NewSessionManager(sessionClient)
	if err != nil {
		return errors.New("Codex App Server session manager could not start")
	}
	defer sessions.Close()
	initCtx, cancelInit := context.WithTimeout(ctx, 15*time.Second)
	err = sessionClient.Initialize(initCtx)
	cancelInit()
	if err != nil {
		return errors.New("Codex App Server initialization failed; no thread was created")
	}
	recoveryCtx, cancelRecovery := context.WithTimeout(ctx, 10*time.Second)
	_, err = sessions.RecoverListedThreads(recoveryCtx)
	cancelRecovery()
	if err != nil {
		return errors.New("existing local session recovery failed; refusing to serve a partial session list")
	}
	go func() { _ = coordinator.Run(ctx) }()
	handler, err := codexsession.NewSessionHTTPHandler(codexsession.SessionHTTPConfig{
		ListenAddress: config.ListenAddress, Origin: config.Origin, Token: localToken,
		StaticFiles: staticFiles, MaxSSE: 16, FallbackEnabled: false,
	}, coordinator, sessions)
	if err != nil {
		return err
	}
	server := &http.Server{
		Addr: config.ListenAddress, Handler: handler,
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 20 * time.Second,
		IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10,
	}
	listener, err := net.Listen("tcp", config.ListenAddress)
	if err != nil {
		return fmt.Errorf("listen for local session UI: %w", err)
	}
	if tcp, ok := listener.Addr().(*net.TCPAddr); !ok || !tcp.IP.IsLoopback() || !tcp.IP.Equal(net.ParseIP("127.0.0.1")) {
		_ = listener.Close()
		return errors.New("session UI listener did not bind to IPv4 loopback")
	}
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Serve(listener) }()
	log.Printf("local Codex session UI listening on %s; local auth token is held in an HttpOnly cookie", config.ListenAddress)
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
		return ctx.Err()
	case err := <-serverDone:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("local session UI server stopped: %w", err)
	}
}

func requireDirectory(path string) error {
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return errors.New("path is not an existing directory")
	}
	return nil
}
