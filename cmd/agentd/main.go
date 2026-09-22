package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/WilliamLi0623/codex-homelab/internal/agentd"
	"github.com/WilliamLi0623/codex-homelab/internal/git"
	"github.com/WilliamLi0623/codex-homelab/internal/muse"
	"github.com/WilliamLi0623/codex-homelab/internal/worker"
	"github.com/WilliamLi0623/codex-homelab/internal/workspace"
)

type request struct {
	Prompt string `json:"prompt"`
}
type response struct {
	ThreadID  string   `json:"thread_id"`
	Events    []string `json:"events"`
	CommitSHA string   `json:"commit_sha,omitempty"`
}

const maxRequestLine = 1 << 20

type runner interface {
	StartNewTurn(context.Context, string) (string, []agentd.Event, error)
	ResumeThread(context.Context, string) error
	StartTurn(context.Context, string, string) (string, error)
	CollectTurnEvents(context.Context) ([]agentd.Event, error)
}

type session struct {
	runner        runner
	mu            sync.Mutex
	thread        string
	result        *response
	commitSHAFile string
	environment   map[string]string
	commitRunner  git.Runner
}

func newSession(runner runner) *session {
	return newSessionWithEnvironment(runner, os.Environ(), nil)
}

func newSessionWithEnvironment(runner runner, environment []string, commitRunner git.Runner) *session {
	values := make(map[string]string)
	for _, entry := range environment {
		name, value, ok := strings.Cut(entry, "=")
		if ok {
			values[name] = value
		}
	}
	return &session{runner: runner, commitSHAFile: values["CODEX_COMMIT_SHA_FILE"], environment: values, commitRunner: commitRunner}
}

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
		result, err := s.resultFor(ctx, thread, events)
		if err != nil {
			return response{}, err
		}
		s.result = &result
		return result, nil
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
	result, err := s.resultFor(ctx, s.thread, events)
	if err != nil {
		return response{}, err
	}
	s.result = &result
	return result, nil
}

func (s *session) resultFor(ctx context.Context, thread string, events []agentd.Event) (response, error) {
	if isToolLoopValues(s.environment) && !containsEvent(events, "muse.tool_call") {
		return response{}, errors.New("tool-loop completed without terminal tool call")
	}
	commitSHA, err := worker.CommitConfiguredWorkspace(ctx, s.environment, s.commitRunner)
	if err != nil {
		return response{}, err
	}
	if commitSHA == "" {
		commitSHA = readCommitSHA(s.commitSHAFile)
	}
	return response{ThreadID: thread, Events: summarizeEvents(events), CommitSHA: commitSHA}, nil
}

func containsEvent(events []agentd.Event, method string) bool {
	for _, event := range events {
		if event.Method == method {
			return true
		}
	}
	return false
}

func (s *session) lastResult() (response, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.result == nil {
		return response{}, false
	}
	return *s.result, true
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

type museRunner struct {
	runner  *muse.Runner
	thread  string
	pending []agentd.Event
}

func (r *museRunner) StartNewTurn(ctx context.Context, prompt string) (string, []agentd.Event, error) {
	result, err := r.runner.Run(ctx, prompt)
	if err != nil {
		return "", nil, err
	}
	r.thread = result.ResponseID
	return r.thread, toAgentdEvents(result.Events), nil
}

func (r *museRunner) ResumeThread(_ context.Context, thread string) error {
	if r.thread == "" || thread != r.thread {
		return fmt.Errorf("Muse response thread %q is unavailable", thread)
	}
	return nil
}

func (r *museRunner) StartTurn(ctx context.Context, _ string, prompt string) (string, error) {
	result, err := r.runner.Run(ctx, prompt)
	if err != nil {
		return "", err
	}
	r.thread = result.ResponseID
	r.pending = toAgentdEvents(result.Events)
	return result.ResponseID, nil
}

func (r *museRunner) CollectTurnEvents(_ context.Context) ([]agentd.Event, error) {
	events := r.pending
	r.pending = nil
	return events, nil
}

func toAgentdEvents(events []muse.Event) []agentd.Event {
	converted := make([]agentd.Event, 0, len(events))
	for _, event := range events {
		converted = append(converted, agentd.Event{Method: event.Method, Params: event.Params})
	}
	return converted
}

func newConfiguredRunner(environment []string) (runner, error) {
	values := environmentValues(environment)
	model := strings.TrimSpace(values["CODEX_MODEL"])
	profile := strings.TrimSpace(values["CODEX_MODEL_PROFILE"])
	loopModel := model
	if loopModel != "muse-spark-1.3-contributor" && loopModel != "glm-5.3-flash" {
		loopModel = profile
	}
	if loopModel != "muse-spark-1.3-contributor" && loopModel != "glm-5.3-flash" {
		return nil, errors.New("tool-loop runner is not configured for this model profile")
	}
	workspace := strings.TrimSpace(values["CODEX_WORKSPACE"])
	if workspace == "" {
		return nil, errors.New("tool-loop runner requires CODEX_WORKSPACE")
	}
	if strings.TrimSpace(values["CODEX_OPENAI_BASE_URL"]) == "" || strings.TrimSpace(values["CODEX_API_KEY"]) == "" {
		return nil, errors.New("tool-loop runner requires CODEX_OPENAI_BASE_URL and CODEX_API_KEY")
	}
	baseURL := values["CODEX_OPENAI_BASE_URL"]
	var client muse.ResponsesClient
	manualContinuation := false
	switch values["CODEX_WIRE_API"] {
	case "responses":
		if loopModel != "muse-spark-1.3-contributor" {
			return nil, errors.New("GLM requires CODEX_WIRE_API=chat-completions")
		}
		client = muse.NewHTTPClient(baseURL, values["CODEX_API_KEY"], nil)
		manualContinuation = isCCHResponsesEndpoint(baseURL)
	case "chat-completions":
		client = muse.NewChatHTTPClient(baseURL, values["CODEX_API_KEY"], nil)
		manualContinuation = true
	default:
		return nil, errors.New("tool-loop runner requires CODEX_WIRE_API=responses or chat-completions")
	}
	museLoop := muse.NewRunner(client, muse.NewTerminal(workspace), loopModel)
	museLoop.ManualContinuation = manualContinuation
	return &museRunner{runner: museLoop}, nil
}

func isCCHResponsesEndpoint(baseURL string) bool {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	return err == nil && strings.EqualFold(parsed.Hostname(), "cch-jp.zenkexi.com")
}

func main() {
	codex := flag.String("codex", "codex", "Codex executable")
	listen := flag.String("listen", "", "HTTP listen address")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	environment := os.Environ()
	if err := validateEnvironment(environment); err != nil {
		fatal(err)
	}
	if err := prepareConfiguredWorkspace(ctx, environment, nil); err != nil {
		fatal(err)
	}
	var configuredRunner runner
	var process *agentd.Process
	var err error
	if isToolLoopEnvironment(environment) {
		configuredRunner, err = newConfiguredRunner(environment)
		if err != nil {
			fatal(err)
		}
	} else {
		if err := ensureCodexConfig(environment); err != nil {
			fatal(err)
		}
		process, err = agentd.StartCodexAppServer(ctx, *codex, environment)
		if err != nil {
			fatal(err)
		}
		defer func() { _ = process.Close() }()
		configuredRunner = clientRunner{client: process.Client}
	}
	session := newSession(configuredRunner)
	server := startHTTPServer(ctx, *listen, session)
	if server != nil {
		if raw := os.Getenv("CODEX_AGENTD_REQUEST"); raw != "" {
			input, err := decodeEnvironmentRequest(raw)
			if err != nil {
				fatal(errors.New("invalid CODEX_AGENTD_REQUEST"))
			}
			if _, err := session.run(ctx, input); err != nil {
				fatal(fmt.Errorf("initial agentd request failed: %w", err))
			}
		}
		defer func() { _ = server.Shutdown(context.Background()) }()
		waitForHTTPServer(ctx)
		return
	}
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

func waitForHTTPServer(ctx context.Context) {
	<-ctx.Done()
}

func newHTTPHandler(session *session) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/healthz", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeHTTPError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("/v1/result", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeHTTPError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		result, ok := session.lastResult()
		if !ok {
			writeHTTPError(w, http.StatusNotFound, "result not found")
			return
		}
		writeJSON(w, http.StatusOK, result)
	})
	mux.HandleFunc("/v1/messages", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeHTTPError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		reader := http.MaxBytesReader(w, r.Body, maxRequestLine)
		defer r.Body.Close()
		decoder := json.NewDecoder(reader)
		decoder.DisallowUnknownFields()
		var input request
		if err := decoder.Decode(&input); err != nil {
			writeHTTPError(w, http.StatusBadRequest, "invalid request")
			return
		}
		if err := decoder.Decode(&struct{}{}); err != io.EOF {
			writeHTTPError(w, http.StatusBadRequest, "invalid request")
			return
		}
		if strings.TrimSpace(input.Prompt) == "" {
			writeHTTPError(w, http.StatusBadRequest, "invalid request")
			return
		}
		result, err := session.run(r.Context(), input)
		if err != nil {
			if strings.Contains(err.Error(), "already in progress") {
				writeHTTPError(w, http.StatusConflict, "request already in progress")
				return
			}
			writeHTTPError(w, http.StatusInternalServerError, "runner error")
			return
		}
		writeJSON(w, http.StatusOK, result)
	})
	return mux
}

func startHTTPServer(ctx context.Context, address string, session *session) *http.Server {
	if strings.TrimSpace(address) == "" {
		return nil
	}
	server := &http.Server{Addr: address, Handler: newHTTPHandler(session)}
	go func() {
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			_, _ = fmt.Fprintln(os.Stderr, "agentd HTTP server stopped")
		}
	}()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	return server
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeHTTPError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
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

func decodeEnvironmentRequest(raw string) (request, error) {
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	var input request
	if err := decoder.Decode(&input); err != nil {
		return request{}, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return request{}, errors.New("environment request must contain one JSON object")
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

func prepareConfiguredWorkspace(ctx context.Context, environment []string, runner workspace.Runner) error {
	values := make(map[string]string)
	for _, entry := range environment {
		name, value, ok := strings.Cut(entry, "=")
		if ok {
			values[name] = value
		}
	}
	repository := strings.TrimSpace(values["CODEX_REPOSITORY"])
	baseRef := strings.TrimSpace(values["CODEX_BASE_REF"])
	workspacePath := strings.TrimSpace(values["CODEX_WORKSPACE"])
	configured := 0
	for _, value := range []string{repository, baseRef, workspacePath} {
		if value != "" {
			configured++
		}
	}
	if configured == 0 {
		return nil
	}
	if configured != 3 {
		return errors.New("agentd workspace configuration is incomplete")
	}
	if err := workspace.Prepare(ctx, repository, baseRef, workspacePath, values["CODEX_ATTEMPT_ID"], runner); err != nil {
		return fmt.Errorf("agentd workspace preparation failed: %w", err)
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

func readCommitSHA(filename string) string {
	if !filepath.IsAbs(filename) {
		return ""
	}
	contents, err := os.ReadFile(filename)
	if err != nil || len(contents) != 40 {
		return ""
	}
	if _, err := hex.DecodeString(string(contents)); err != nil {
		return ""
	}
	return string(contents)
}

func fatal(err error) { _, _ = fmt.Fprintln(os.Stderr, err); os.Exit(1) }
