package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/WilliamLi0623/codex-homelab/internal/responsesbridge"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:17842", "loopback listen address")
	upstream := flag.String("upstream", os.Getenv("CCH_CHAT_URL"), "CC Hub Chat Completions base URL")
	model := flag.String("model", responsesbridge.DefaultModel, "allowed upstream model")
	flag.Parse()
	if err := validateLoopback(*listen); err != nil {
		log.Fatal(err)
	}
	if *upstream == "" {
		log.Fatal("CCH_CHAT_URL or -upstream is required")
	}
	userAgent := strings.TrimSpace(os.Getenv("CCH_USER_AGENT"))
	if err := validateCodingAgentUserAgent(userAgent); err != nil {
		log.Fatal(err)
	}
	bridge := responsesbridge.NewBridge(responsesbridge.BridgeConfig{
		UpstreamURL:     *upstream,
		APIKey:          os.Getenv("CCH_API_KEY"),
		UserAgent:       userAgent,
		Model:           *model,
		UpstreamTimeout: 90 * time.Second,
	})
	server := &http.Server{Addr: *listen, Handler: bridge, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 2 * time.Minute, IdleTimeout: 30 * time.Second}
	log.Printf("responses bridge listening on %s", *listen)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

func validateCodingAgentUserAgent(value string) error {
	if !strings.HasPrefix(value, "codex_cli_rs/") || !strings.Contains(value, " (") || !strings.Contains(value, "; ") || !strings.Contains(value, ") ") {
		return fmt.Errorf("CCH_USER_AGENT must use the configured codex_cli_rs/<version> (<OS> <version>; <architecture>) <terminal> format")
	}
	return nil
}

func validateLoopback(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("invalid listen address: %w", err)
	}
	if host != "127.0.0.1" && host != "::1" {
		return fmt.Errorf("responses bridge must bind to loopback, got %q", host)
	}
	return nil
}
