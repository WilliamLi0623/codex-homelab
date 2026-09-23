package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestNewGatewayHTTPServerUsesBoundedLongOperationTimeout(t *testing.T) {
	server := newGatewayHTTPServer("127.0.0.1:8090", http.NewServeMux())
	if server.WriteTimeout < 2*time.Minute {
		t.Fatalf("WriteTimeout = %s, want at least 2m for dynamic dispatch", server.WriteTimeout)
	}
	if server.WriteTimeout > 5*time.Minute {
		t.Fatalf("WriteTimeout = %s, want a bounded timeout no longer than 5m", server.WriteTimeout)
	}
}

func TestLoadGatewayConfigDefaultsToLoopback(t *testing.T) {
	config, err := loadGatewayConfig(func(name string) string {
		return map[string]string{
			"MCP_GATEWAY_DATABASE": "controller.sqlite",
			"MCP_GATEWAY_TOKEN":    "secret",
		}[name]
	}, "", "", "")
	if err != nil {
		t.Fatalf("loadGatewayConfig() error = %v", err)
	}
	if config.ListenAddress != defaultListenAddress {
		t.Fatalf("listen address = %q, want %q", config.ListenAddress, defaultListenAddress)
	}
}

func TestLoadGatewayConfigRequiresDatabaseAndToken(t *testing.T) {
	if _, err := loadGatewayConfig(func(string) string { return "" }, "", "", ""); err == nil {
		t.Fatal("loadGatewayConfig() accepted missing database and token")
	}
	if _, err := loadGatewayConfig(func(string) string { return "" }, "127.0.0.1:8090", "db.sqlite", "secret"); err != nil {
		t.Fatalf("explicit config error = %v", err)
	}
}

func TestLoadGatewayConfigRejectsPublicListenAddresses(t *testing.T) {
	for _, address := range []string{"0.0.0.0:8090", ":8090", "[::]:8090"} {
		if _, err := loadGatewayConfig(func(string) string { return "" }, address, "db.sqlite", "secret"); err == nil {
			t.Fatalf("loadGatewayConfig() accepted public address %q", address)
		}
	}
}

func TestBearerAuthenticator(t *testing.T) {
	auth := bearerAuthenticator{token: "secret"}
	request := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	if err := auth.Authenticate(request); err == nil {
		t.Fatal("Authenticate() accepted missing authorization")
	}
	request.Header.Set("Authorization", "Bearer secret")
	if err := auth.Authenticate(request); err != nil {
		t.Fatalf("Authenticate() rejected valid token: %v", err)
	}
	request.Header.Set("Authorization", "Bearer wrong")
	if err := auth.Authenticate(request); err == nil {
		t.Fatal("Authenticate() accepted wrong token")
	}
}
