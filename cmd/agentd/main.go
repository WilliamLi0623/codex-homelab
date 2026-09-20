package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
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

type runner interface {
	StartNewTurn(context.Context, string) (string, []agentd.Event, error)
	ResumeThread(context.Context, string) error
	StartTurn(context.Context, string, string) (string, error)
	CollectTurnEvents(context.Context) ([]agentd.Event, error)
}

type session struct {
	runner runner
	mu     sync.Mutex
	thread string
}

func newSession(runner runner) *session { return &session{runner: runner} }

func (s *session) run(ctx context.Context, input request) (response, error) {
	if !s.mu.TryLock() {
		return response{}, errors.New("agentd request already in progress")
	}
	defer s.mu.Unlock()
	if s.thread == "" {
		thread, events, err := s.runner.StartNewTurn(ctx, input.Prompt)
		if err != nil {
			return response{}, err
		}
		s.thread = thread
		return response{ThreadID: thread, Events: summarizeEvents(events)}, nil
	}
	if err := s.runner.ResumeThread(ctx, s.thread); err != nil {
		return response{}, err
	}
	if _, err := s.runner.StartTurn(ctx, s.thread, input.Prompt); err != nil {
		return response{}, err
	}
	events, err := s.runner.CollectTurnEvents(ctx)
	if err != nil {
		return response{}, err
	}
	return response{ThreadID: s.thread, Events: summarizeEvents(events)}, nil
}

type clientRunner struct{ client *agentd.Client }

func (r clientRunner) StartNewTurn(ctx context.Context, prompt string) (string, []agentd.Event, error) {
	return r.client.StartNewTurn(ctx, prompt)
}
func (r clientRunner) ResumeThread(ctx context.Context, thread string) error {
	return r.client.ResumeThread(ctx, thread)
}
func (r clientRunner) StartTurn(ctx context.Context, thread, prompt string) (string, error) {
	return r.client.StartTurn(ctx, thread, prompt)
}
func (r clientRunner) CollectTurnEvents(ctx context.Context) ([]agentd.Event, error) {
	return r.client.CollectTurnEvents(ctx)
}

func main() {
	codex := flag.String("codex", "codex", "Codex executable")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	process, err := agentd.StartCodexAppServer(ctx, *codex, os.Environ())
	if err != nil {
		fatal(err)
	}
	defer func() { _ = process.Close() }()
	session := newSession(clientRunner{client: process.Client})
	output := bufio.NewWriter(os.Stdout)
	encoder := json.NewEncoder(output)
	decoder := json.NewDecoder(io.LimitReader(os.Stdin, 1<<20))
	decoder.DisallowUnknownFields()
	for {
		input, err := decodeNextRequest(decoder)
		if errors.Is(err, io.EOF) {
			return
		}
		if err != nil {
			fatal(err)
		}
		result, err := session.run(ctx, input)
		if err != nil {
			fatal(err)
		}
		if err := encoder.Encode(result); err != nil {
			fatal(err)
		}
		if err := output.Flush(); err != nil {
			fatal(err)
		}
	}
}

func decodeRequest(reader io.Reader) (request, error) {
	decoder := json.NewDecoder(io.LimitReader(reader, 1<<20))
	decoder.DisallowUnknownFields()
	input, err := decodeNextRequest(decoder)
	if err != nil {
		return request{}, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return request{}, errors.New("worker request must contain one JSON object")
	}
	return input, nil
}

func decodeNextRequest(decoder *json.Decoder) (request, error) {
	var input request
	if err := decoder.Decode(&input); err != nil {
		if errors.Is(err, io.EOF) {
			return request{}, io.EOF
		}
		return request{}, fmt.Errorf("decode worker request: %w", err)
	}
	if strings.TrimSpace(input.Prompt) == "" {
		return request{}, errors.New("worker prompt is required")
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
