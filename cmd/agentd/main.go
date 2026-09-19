package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/WilliamLi0623/codex-homelab/internal/agentd"
)

type request struct {
	Prompt string `json:"prompt"`
}
type response struct {
	ThreadID string   `json:"thread_id"`
	Events   []string `json:"events"`
}

func main() {
	codex := flag.String("codex", "codex", "Codex executable")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	input, err := decodeRequest(os.Stdin)
	if err != nil {
		fatal(err)
	}
	process, err := agentd.StartCodexAppServer(ctx, *codex, os.Environ())
	if err != nil {
		fatal(err)
	}
	defer func() { _ = process.Close() }()
	threadID, events, err := process.Client.StartNewTurn(ctx, input.Prompt)
	if err != nil {
		fatal(err)
	}
	if err := json.NewEncoder(os.Stdout).Encode(response{ThreadID: threadID, Events: summarizeEvents(events)}); err != nil {
		fatal(err)
	}
}

func decodeRequest(reader io.Reader) (request, error) {
	var input request
	decoder := json.NewDecoder(io.LimitReader(reader, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return request{}, fmt.Errorf("decode worker request: %w", err)
	}
	if strings.TrimSpace(input.Prompt) == "" {
		return request{}, errors.New("worker prompt is required")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return request{}, errors.New("worker request must contain one JSON object")
	}
	return input, nil
}
func summarizeEvents(events []agentd.Event) []string {
	methods := make([]string, 0, len(events))
	for _, event := range events {
		if event.Method != "" {
			methods = append(methods, event.Method)
		}
	}
	return methods
}
func fatal(err error) { _, _ = fmt.Fprintln(os.Stderr, err); os.Exit(1) }
