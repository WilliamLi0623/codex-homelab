package main

import (
	"strings"
	"testing"
)

func TestControllerRoutingStateURL(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
		bad   bool
	}{
		{name: "https accepted", input: "https://controller.example", want: "https://controller.example/internal/v1/routing-state"},
		{name: "remote http rejected", input: "http://100.64.0.10:18080/prefix", bad: true},
		{name: "loopback ipv4 http accepted", input: "http://127.0.0.1:18080/prefix", want: "http://127.0.0.1:18080/prefix/internal/v1/routing-state"},
		{name: "loopback ipv6 http accepted", input: "http://[::1]:18080", want: "http://[::1]:18080/internal/v1/routing-state"},
		{name: "localhost http accepted", input: "http://LOCALHOST:18080", want: "http://LOCALHOST:18080/internal/v1/routing-state"},
		{name: "https existing path", input: "https://controller.example/internal/v1/routing-state", want: "https://controller.example/internal/v1/routing-state"},
		{name: "credentials rejected", input: "https://user:pass@controller.example", bad: true},
		{name: "query rejected", input: "https://controller.example/?token=secret", bad: true},
		{name: "malformed url rejected", input: "https://%zz", bad: true},
		{name: "scheme rejected", input: "file:///tmp/controller", bad: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := controllerRoutingStateURL(tt.input)
			if (err != nil) != tt.bad {
				t.Fatalf("controllerRoutingStateURL() error = %v, bad=%t", err, tt.bad)
			}
			if !tt.bad && got != tt.want {
				t.Fatalf("controllerRoutingStateURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSafeAppServerEnvironmentExcludesPublisherCredentials(t *testing.T) {
	t.Setenv("CODEX_ROUTING_STATE_TOKEN", "not-for-child")
	t.Setenv("CCH_API_KEY", "not-for-child-either")
	for _, entry := range safeAppServerEnvironment() {
		if strings.HasPrefix(strings.ToUpper(entry), "CODEX_ROUTING_STATE_TOKEN=") || strings.HasPrefix(strings.ToUpper(entry), "CCH_API_KEY=") {
			t.Fatalf("sensitive parent variable propagated to quota App Server: %q", entry)
		}
	}
}
