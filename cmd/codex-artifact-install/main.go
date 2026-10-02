package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/WilliamLi0623/codex-homelab/internal/sessionruntime"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	observe, digest, ok := parseArgs(os.Args)
	if !ok {
		fail()
	}
	var evidenceSHA string
	var err error
	if observe {
		result, observeErr := sessionruntime.ObserveBootstrapCodexArtifact(ctx, digest)
		evidenceSHA, err = result.SHA256, observeErr
	} else {
		result, installErr := sessionruntime.InstallBootstrapCodexArtifact(ctx, os.Stdin, digest)
		evidenceSHA, err = result.SHA256, installErr
	}
	if err != nil || len(evidenceSHA) != 64 {
		fail()
	}
	if _, err := fmt.Fprintf(os.Stdout, "P28_CODEX_INSTALL_COMPLETE:%s\n", evidenceSHA); err != nil {
		fail()
	}
}

func parseArgs(args []string) (observe bool, digest string, ok bool) {
	if len(args) == 2 {
		return false, args[1], true
	}
	if len(args) == 3 && args[1] == "--observe" {
		return true, args[2], true
	}
	return false, "", false
}

func fail() {
	_, _ = fmt.Fprintln(os.Stderr, "Codex artifact install failed")
	os.Exit(1)
}
