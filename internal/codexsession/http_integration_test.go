package codexsession

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/WilliamLi0623/codex-homelab/internal/codexrouting"
)

type integrationQuotaReader struct {
	mu        sync.Mutex
	snapshots []codexrouting.QuotaSnapshot
}

func (r *integrationQuotaReader) Read(ctx context.Context) (codexrouting.QuotaSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return codexrouting.QuotaSnapshot{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.snapshots) == 0 {
		return codexrouting.QuotaSnapshot{}, errors.New("no quota fixture remains")
	}
	snapshot := r.snapshots[0]
	r.snapshots = r.snapshots[1:]
	return snapshot, nil
}

type integrationRouteStore struct {
	mu    sync.Mutex
	state codexrouting.RoutingState
	ok    bool
}

func (s *integrationRouteStore) Load(context.Context) (codexrouting.RoutingState, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state, s.ok, nil
}

func (s *integrationRouteStore) Save(_ context.Context, state codexrouting.RoutingState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state, s.ok = state, true
	return nil
}

type integrationRouteSink struct {
	mu     sync.Mutex
	states []codexrouting.RoutingState
	err    error
}

func (s *integrationRouteSink) Publish(_ context.Context, state codexrouting.RoutingState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	s.states = append(s.states, state)
	return nil
}

func TestSessionHTTPCoordinatorAppServerIntegrationPinsRoutesAndFailsClosed(t *testing.T) {
	reader := &integrationQuotaReader{snapshots: []codexrouting.QuotaSnapshot{
		{OrdinaryUsageAllowed: boolPtr(true)},
		{OrdinaryUsageAllowed: boolPtr(false)},
		{OrdinaryUsageAllowed: boolPtr(true)},
	}}
	sink := &integrationRouteSink{}
	store := &integrationRouteStore{}
	coordinator, err := codexrouting.NewCoordinator(reader, sink, store, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	client, serverConn := newFakeAppServerClient(t)
	manager, err := NewSessionManager(client)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)
	serverResult := make(chan error, 1)
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		serverResult <- serveSessionIntegrationAppServer(t, serverConn)
	}()

	handler, err := NewSessionHTTPHandler(SessionHTTPConfig{
		ListenAddress: "127.0.0.1:8765", Origin: "http://127.0.0.1:8765",
		Token: strings.Repeat("z", 32), MaxSSE: 2, FallbackEnabled: true,
	}, coordinator, manager)
	if err != nil {
		t.Fatal(err)
	}
	cookie := localUICookie(t, handler)
	first := authorizedUIRequest(handler, http.MethodPost, "/api/sessions", strings.NewReader(sessionCreateBody(t, "normal first")), cookie)
	if first.Code != http.StatusCreated {
		t.Fatalf("normal session status=%d body=%s", first.Code, first.Body.String())
	}
	second := authorizedUIRequest(handler, http.MethodPost, "/api/sessions", strings.NewReader(sessionCreateBody(t, "fallback second")), cookie)
	if second.Code != http.StatusCreated {
		t.Fatalf("fallback session status=%d body=%s", second.Code, second.Body.String())
	}
	if got := manager.Get("thread-1"); got == nil || got.Pin.Provider != "openai" || got.Pin.Generation != 1 {
		t.Fatalf("first thread lost its normal route: %+v", got)
	}
	if got := manager.Get("thread-2"); got == nil || got.Pin.Provider != "osc" || got.Pin.Model != "muse-spark-1.3-contributor" || got.Pin.Generation != 2 {
		t.Fatalf("second thread did not receive fallback route: %+v", got)
	}
	resumed, err := manager.ResumeThread(context.Background(), "thread-1")
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Pin.Provider != "openai" || resumed.Pin.Model != "gpt-6-luna" || resumed.Pin.Generation != 1 {
		t.Fatalf("existing thread changed route after quota transition: %+v", resumed.Pin)
	}

	sink.mu.Lock()
	sink.err = errors.New("private Controller failure detail")
	sink.mu.Unlock()
	third := authorizedUIRequest(handler, http.MethodPost, "/api/sessions", strings.NewReader(sessionCreateBody(t, "blocked third")), cookie)
	if third.Code != http.StatusServiceUnavailable || strings.Contains(third.Body.String(), "private Controller") || manager.Get("thread-3") != nil {
		t.Fatalf("publication failure was not fail-closed: status=%d body=%s", third.Code, third.Body.String())
	}
	select {
	case err := <-serverResult:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("fake App Server did not process all expected requests")
	}
	<-serverDone
}

func boolPtr(value bool) *bool { return &value }

func serveSessionIntegrationAppServer(t *testing.T, conn net.Conn) error {
	t.Helper()
	reader := bufio.NewReader(conn)
	threads := make(map[string]map[string]any)
	threadNumber, turnNumber := 0, 0
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			return err
		}
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params map[string]any  `json:"params"`
		}
		if err := json.Unmarshal(line, &request); err != nil {
			return err
		}
		switch request.Method {
		case "initialize":
			writeAppServerResponse(t, conn, request.ID, map[string]any{})
		case "initialized":
			continue
		case "thread/start":
			threadNumber++
			provider, _ := request.Params["modelProvider"].(string)
			model, _ := request.Params["model"].(string)
			config, _ := request.Params["config"].(map[string]any)
			effort, _ := config["model_reasoning_effort"].(string)
			id := "thread-" + string(rune('0'+threadNumber))
			thread := map[string]any{"id": id, "modelProvider": provider, "model": model, "reasoningEffort": effort, "threadSource": sessionUIServiceName}
			threads[id] = thread
			writeAppServerResponse(t, conn, request.ID, map[string]any{"thread": thread, "modelProvider": provider, "model": model, "reasoningEffort": effort, "approvalPolicy": "on-request"})
		case "turn/start":
			turnNumber++
			writeAppServerResponse(t, conn, request.ID, map[string]any{"turn": map[string]any{"id": "turn-" + string(rune('0'+turnNumber))}})
		case "thread/resume":
			id, _ := request.Params["threadId"].(string)
			thread, exists := threads[id]
			if !exists {
				return errors.New("unexpected thread resume")
			}
			provider, _ := thread["modelProvider"].(string)
			model, _ := thread["model"].(string)
			effort, _ := thread["reasoningEffort"].(string)
			writeAppServerResponse(t, conn, request.ID, map[string]any{"thread": thread, "modelProvider": provider, "model": model, "reasoningEffort": effort, "approvalPolicy": "on-request"})
			return nil
		default:
			return errors.New("unexpected App Server method: " + request.Method)
		}
	}
}
