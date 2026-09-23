package main

import "testing"

func TestValidateLoopbackRejectsPublicListeners(t *testing.T) {
	for _, address := range []string{"0.0.0.0:17842", ":17842", "10.0.0.1:17842", "127.0.0.2:17842", "[::2]:17842"} {
		if err := validateLoopback(address); err == nil {
			t.Errorf("validateLoopback(%q) accepted public listener", address)
		}
	}
	for _, address := range []string{"127.0.0.1:17842", "[::1]:17842"} {
		if err := validateLoopback(address); err != nil {
			t.Errorf("validateLoopback(%q) rejected loopback: %v", address, err)
		}
	}
	if err := validateLoopback("localhost:17842"); err == nil {
		t.Fatal("localhost must not bypass literal loopback restriction")
	}
}

func TestValidateCodingAgentUserAgent(t *testing.T) {
	if err := validateCodingAgentUserAgent("codex_cli_rs/0.156.1 (Ubuntu 24.04; x86_64) bash/5.2"); err != nil {
		t.Fatalf("valid coding-agent UA rejected: %v", err)
	}
	for _, value := range []string{"", "Go-http-client/1.1", "codex_cli_rs/0.156.1"} {
		if err := validateCodingAgentUserAgent(value); err == nil {
			t.Errorf("invalid UA %q accepted", value)
		}
	}
}
