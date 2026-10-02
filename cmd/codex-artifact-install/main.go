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
	var evidenceSHA string
	var err error
	switch {
	case len(os.Args) == 2:
		result, installErr := sessionruntime.InstallBootstrapCodexArtifact(ctx, os.Stdin, os.Args[1])
		evidenceSHA, err = result.SHA256, installErr
	case len(os.Args) == 3 && os.Args[1] == "--observe":
		result, observeErr := sessionruntime.ObserveBootstrapCodexArtifact(ctx, os.Args[2])
		evidenceSHA, err = result.SHA256, observeErr
	default:
		fail()
	}
	if err != nil || len(evidenceSHA) != 64 {
		fail()
	}
	if _, err := fmt.Fprintf(os.Stdout, "P28_CODEX_INSTALL_COMPLETE:%s\n", evidenceSHA); err != nil {
		fail()
	}
}

func fail() {
	_, _ = fmt.Fprintln(os.Stderr, "Codex artifact install failed")
	os.Exit(1)
}
