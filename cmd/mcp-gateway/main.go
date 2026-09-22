package main

import (
	"crypto/subtle"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/WilliamLi0623/codex-homelab/internal/mcp"
	"github.com/WilliamLi0623/codex-homelab/internal/store"
)

const defaultListenAddress = "127.0.0.1:8090"

type gatewayConfig struct {
	ListenAddress   string
	DatabasePath    string
	AuthToken       string
	ControllerURL   string
	ControllerToken string
}

func main() {
	listenAddress := flag.String("listen", "", "private HTTP listen address; defaults to MCP_GATEWAY_LISTEN or loopback")
	databasePath := flag.String("database", "", "Controller SQLite path; defaults to MCP_GATEWAY_DATABASE")
	authToken := flag.String("token", "", "Bearer token; defaults to MCP_GATEWAY_TOKEN")
	controllerURL := flag.String("controller", "", "optional Controller URL; defaults to MCP_GATEWAY_CONTROLLER_URL")
	controllerToken := flag.String("controller-token", "", "optional Controller Bearer token; defaults to MCP_GATEWAY_CONTROLLER_TOKEN")
	flag.Parse()

	config, err := loadGatewayConfig(func(name string) string { return os.Getenv(name) }, *listenAddress, *databasePath, *authToken, *controllerURL, *controllerToken)
	if err != nil {
		log.Fatal(err)
	}
	handler, closeStore, err := newHandler(config)
	if err != nil {
		log.Fatal(err)
	}
	defer closeStore()

	server := &http.Server{
		Addr:              config.ListenAddress,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	log.Printf("MCP gateway listening on %s", config.ListenAddress)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func loadGatewayConfig(getenv func(string) string, flagListen, flagDatabase, flagToken string, controllerFlags ...string) (gatewayConfig, error) {
	listenAddress := strings.TrimSpace(flagListen)
	if listenAddress == "" {
		listenAddress = strings.TrimSpace(getenv("MCP_GATEWAY_LISTEN"))
	}
	if listenAddress == "" {
		listenAddress = defaultListenAddress
	}
	if err := validatePrivateListenAddress(listenAddress); err != nil {
		return gatewayConfig{}, err
	}

	databasePath := strings.TrimSpace(flagDatabase)
	if databasePath == "" {
		databasePath = strings.TrimSpace(getenv("MCP_GATEWAY_DATABASE"))
	}
	if databasePath == "" {
		return gatewayConfig{}, fmt.Errorf("MCP_GATEWAY_DATABASE is required")
	}
	authToken := strings.TrimSpace(flagToken)
	if authToken == "" {
		authToken = strings.TrimSpace(getenv("MCP_GATEWAY_TOKEN"))
	}
	if authToken == "" {
		return gatewayConfig{}, fmt.Errorf("MCP_GATEWAY_TOKEN is required")
	}
	var flagController, flagControllerToken string
	if len(controllerFlags) > 0 {
		flagController = controllerFlags[0]
	}
	if len(controllerFlags) > 1 {
		flagControllerToken = controllerFlags[1]
	}
	controllerURL := strings.TrimSpace(flagController)
	if controllerURL == "" {
		controllerURL = strings.TrimSpace(getenv("MCP_GATEWAY_CONTROLLER_URL"))
	}
	controllerToken := strings.TrimSpace(flagControllerToken)
	if controllerToken == "" {
		controllerToken = strings.TrimSpace(getenv("MCP_GATEWAY_CONTROLLER_TOKEN"))
	}
	return gatewayConfig{ListenAddress: listenAddress, DatabasePath: databasePath, AuthToken: authToken, ControllerURL: controllerURL, ControllerToken: controllerToken}, nil
}

func validatePrivateListenAddress(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil || strings.TrimSpace(host) == "" {
		return fmt.Errorf("MCP gateway listen address must include a private host and port")
	}
	switch strings.Trim(host, "[]") {
	case "0.0.0.0", "::", "*":
		return fmt.Errorf("MCP gateway must not listen on all interfaces")
	}
	return nil
}

func newHandler(config gatewayConfig) (http.Handler, func(), error) {
	database, err := store.Open(config.DatabasePath)
	if err != nil {
		return nil, nil, fmt.Errorf("open controller store: %w", err)
	}
	auth := bearerAuthenticator{token: config.AuthToken}
	server := mcp.NewServer(database)
	if config.ControllerURL != "" {
		client, err := mcp.NewControllerClient(config.ControllerURL, config.ControllerToken, database)
		if err != nil {
			_ = database.Close()
			return nil, nil, fmt.Errorf("configure Controller client: %w", err)
		}
		server = mcp.NewServerWithDispatcherAndMessageSender(database, client, client)
	}
	transport := mcp.NewTransport(server, auth)
	return transport, func() {
		if err := database.Close(); err != nil {
			log.Printf("close controller store: %v", err)
		}
	}, nil
}

type bearerAuthenticator struct {
	token string
}

func (a bearerAuthenticator) Authenticate(request *http.Request) error {
	value := request.Header.Get("Authorization")
	const prefix = "Bearer "
	if !strings.HasPrefix(value, prefix) {
		return mcp.ErrUnauthorized
	}
	provided := strings.TrimSpace(strings.TrimPrefix(value, prefix))
	if subtle.ConstantTimeCompare([]byte(provided), []byte(a.token)) != 1 {
		return mcp.ErrUnauthorized
	}
	return nil
}
