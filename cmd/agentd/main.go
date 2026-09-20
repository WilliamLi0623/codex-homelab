package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path"
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

const maxRequestLine = 1 << 20

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
	environment := os.Environ()
	if err := validateEnvironment(environment); err != nil {
		fatal(err)
	}
	process, err := agentd.StartCodexAppServer(ctx, *codex, environment)
	if err != nil {
		fatal(err)
	}
	defer func() { _ = process.Close() }()
	session := newSession(clientRunner{client: process.Client})
	output := bufio.NewWriter(os.Stdout)
	encoder := json.NewEncoder(output)
	reader := bufio.NewReader(os.Stdin)
	for {
		input, err := decodeNextRequest(reader)
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

func decodeNextRequest(reader *bufio.Reader) (request, error) {
	line, err := readRequestLine(reader)
	if err != nil {
		return request{}, err
	}
	decoder := json.NewDecoder(strings.NewReader(string(line)))
	decoder.DisallowUnknownFields()
	var input request
	if err := decoder.Decode(&input); err != nil {
		return request{}, fmt.Errorf("decode worker request: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return request{}, errors.New("worker request line must contain one JSON object")
	}
	if strings.TrimSpace(input.Prompt) == "" {
		return request{}, errors.New("worker prompt is required")
	}
	return input, nil
}

// decodeRequest is retained for the package's legacy single-request tests.
// The worker entrypoint uses decodeNextRequest directly for its JSONL loop.
func decodeRequest(reader io.Reader) (request, error) {
	buffered := bufio.NewReader(reader)
	input, err := decodeNextRequest(buffered)
	if err != nil {
		return request{}, err
	}
	for {
		line, err := readRequestLine(buffered)
		if errors.Is(err, io.EOF) {
			return input, nil
		}
		if err != nil {
			return request{}, err
		}
		if len(bytes.TrimSpace(line)) != 0 {
			return request{}, errors.New("worker request must contain one JSON object")
		}
	}
}

func readRequestLine(reader *bufio.Reader) ([]byte, error) {
	var line []byte
	for {
		chunk, err := reader.ReadSlice('\n')
		line = append(line, chunk...)
		if len(line) > maxRequestLine {
			return nil, errors.New("worker request exceeds 1 MiB")
		}
		if err == nil {
			return bytes.TrimSuffix(line, []byte{'\n'}), nil
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if errors.Is(err, io.EOF) {
			if len(line) == 0 {
				return nil, io.EOF
			}
			return line, nil
		}
		return nil, fmt.Errorf("read worker request: %w", err)
	}
}

func validateEnvironment(environment []string) error {
	values := make(map[string]string)
	for _, entry := range environment {
		name, value, ok := strings.Cut(entry, "=")
		if ok {
			values[name] = value
		}
	}
	home := values["CODEX_HOME"]
	attemptID := values["CODEX_ATTEMPT_ID"]
	if strings.TrimSpace(home) == "" {
		return errors.New("agentd requires non-empty CODEX_HOME")
	}
	if !safeAttemptID(attemptID) {
		return errors.New("agentd requires non-empty safe CODEX_ATTEMPT_ID")
	}
	if !path.IsAbs(home) {
		return errors.New("agentd requires absolute CODEX_HOME")
	}
	if path.Base(path.Clean(home)) != attemptID {
		return errors.New("agentd requires CODEX_HOME basename to equal CODEX_ATTEMPT_ID")
	}
	return nil
}

func safeAttemptID(value string) bool {
	if value == "" || len(value) > 64 || value == "." || value == ".." {
		return false
	}
	for _, char := range value {
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') &&
			(char < '0' || char > '9') && char != '-' && char != '_' && char != '.' {
			return false
		}
	}
	return true
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
