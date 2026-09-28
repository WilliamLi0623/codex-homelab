package codexsession

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
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

func TestSessionHTTPFullMockedRouteApprovalContinuationAndRecovery(t *testing.T) {
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
	serverDone := make(chan error, 1)
	go func() { serverDone <- serveTask6LifecycleAppServer(t, serverConn) }()

	handler, err := NewSessionHTTPHandler(SessionHTTPConfig{
		ListenAddress: "127.0.0.1:8765", Origin: "http://127.0.0.1:8765",
		Token: strings.Repeat("z", 32), MaxSSE: 2, FallbackEnabled: true,
	}, coordinator, manager)
	if err != nil {
		t.Fatal(err)
	}
	cookie := localUICookie(t, handler)
	first := createHTTPIntegrationSession(t, handler, cookie, "run one harmless step")
	if first.Session.Provider != "openai" || first.Session.Model != "gpt-6-luna" || first.Session.Effort != "high" || first.Session.Generation != 1 {
		t.Fatalf("first session route=%+v", first.Session)
	}

	session := manager.Get(first.Session.ID)
	if session == nil {
		t.Fatal("first App Server thread was not registered")
	}
	streamCtx, cancelStream := context.WithCancel(context.Background())
	streamRequest := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8765/api/sessions/"+first.Session.ID+"/events", nil).WithContext(streamCtx)
	streamRequest.Host = "127.0.0.1:8765"
	streamRequest.AddCookie(cookie)
	streamWriter := newIntegrationStreamWriter()
	streamDone := make(chan struct{})
	go func() {
		defer close(streamDone)
		handler.ServeHTTP(streamWriter, streamRequest)
	}()
	if err := streamWriter.waitFor(`"type":"assistant_delta"`, `"type":"approval"`); err != nil {
		cancelStream()
		t.Fatal(err)
	}
	streamText := streamWriter.String()
	if !strings.Contains(streamText, `"delta":"starting safely"`) || !strings.Contains(streamText, `"command":"printf safe"`) {
		cancelStream()
		t.Fatalf("stream did not surface assistant text and bounded approval: %s", streamText)
	}
	cancelStream()
	select {
	case <-streamDone:
	case <-time.After(time.Second):
		t.Fatal("SSE stream did not stop after UI disconnect")
	}

	approvalID := base64.RawURLEncoding.EncodeToString([]byte(`"approval-1"`))
	approvalBody, _ := json.Marshal(map[string]string{"thread_id": first.Session.ID, "id": approvalID, "decision": "accept"})
	approvalResponse := authorizedUIRequest(handler, http.MethodPost, "/api/approvals", strings.NewReader(string(approvalBody)), cookie)
	if approvalResponse.Code != http.StatusNoContent {
		t.Fatalf("ephemeral approval response status=%d body=%s", approvalResponse.Code, approvalResponse.Body.String())
	}
	select {
	case notice, open := <-session.Notices:
		if !open || notice.Event == nil || notice.Event.Method != "turn/completed" {
			t.Fatalf("first turn completion notice=%+v open=%t", notice, open)
		}
	case <-time.After(time.Second):
		t.Fatal("first turn did not complete after explicit approval")
	}

	continueBody, _ := json.Marshal(map[string]string{"text": "continue on that same thread"})
	continued := authorizedUIRequest(handler, http.MethodPost, "/api/sessions/"+first.Session.ID+"/turns", strings.NewReader(string(continueBody)), cookie)
	if continued.Code != http.StatusAccepted {
		t.Fatalf("same-thread continuation status=%d body=%s", continued.Code, continued.Body.String())
	}
	var continuedTurn struct {
		TurnID string `json:"turn_id"`
	}
	if err := json.Unmarshal(continued.Body.Bytes(), &continuedTurn); err != nil || continuedTurn.TurnID != "turn-2" {
		t.Fatalf("continuation turn=%+v err=%v", continuedTurn, err)
	}
	awaitIntegrationTurnCompletion(t, session)

	fallback := createHTTPIntegrationSession(t, handler, cookie, "fallback is a mock-only route probe")
	if fallback.Session.Provider != "osc" || fallback.Session.Model != "muse-spark-1.3-contributor" || fallback.Session.Effort != "xhigh" || fallback.Session.Generation != 2 {
		t.Fatalf("mock exhaustion did not pin Spark/xhigh: %+v", fallback.Session)
	}
	if firstPinned := manager.Get(first.Session.ID); firstPinned == nil || firstPinned.Pin.Provider != "openai" || firstPinned.Pin.Model != "gpt-6-luna" || firstPinned.Pin.Generation != 1 {
		t.Fatalf("quota transition mutated existing thread pin: %+v", firstPinned)
	}
	awaitIntegrationTurnCompletion(t, manager.Get(fallback.Session.ID))

	recovered := createHTTPIntegrationSession(t, handler, cookie, "quota recovered; new thread only")
	if recovered.Session.Provider != "openai" || recovered.Session.Model != "gpt-6-luna" || recovered.Session.Effort != "high" || recovered.Session.Generation != 3 {
		t.Fatalf("recovered quota route=%+v", recovered.Session)
	}
	awaitIntegrationTurnCompletion(t, manager.Get(recovered.Session.ID))
	resumed, err := manager.ResumeThread(context.Background(), first.Session.ID)
	if err != nil || resumed.Pin.Provider != "openai" || resumed.Pin.Model != "gpt-6-luna" || resumed.Pin.Generation != 1 {
		t.Fatalf("original thread did not resume with its initial pin: pin=%+v err=%v", resumed.Pin, err)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}

type integrationCreatedSession struct {
	Session struct {
		ID         string `json:"id"`
		Provider   string `json:"provider"`
		Model      string `json:"model"`
		Effort     string `json:"effort"`
		Generation uint64 `json:"generation"`
	} `json:"session"`
	TurnID string `json:"turn_id"`
}

func createHTTPIntegrationSession(t *testing.T, handler http.Handler, cookie *http.Cookie, prompt string) integrationCreatedSession {
	t.Helper()
	response := authorizedUIRequest(handler, http.MethodPost, "/api/sessions", strings.NewReader(sessionCreateBody(t, prompt)), cookie)
	if response.Code != http.StatusCreated {
		t.Fatalf("session create status=%d body=%s", response.Code, response.Body.String())
	}
	var created integrationCreatedSession
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	return created
}

func awaitIntegrationTurnCompletion(t *testing.T, session *ManagedSession) {
	t.Helper()
	if session == nil {
		t.Fatal("expected managed session")
	}
	deadline := time.After(time.Second)
	for {
		select {
		case notice, open := <-session.Notices:
			if !open {
				t.Fatal("session stream closed before turn completion")
			}
			if notice.Error != "" {
				t.Fatalf("session stream failed: %s", notice.Error)
			}
			if notice.Event != nil && notice.Event.Method == "turn/completed" {
				return
			}
		case <-deadline:
			t.Fatal("timed out waiting for turn completion")
		}
	}
}

type integrationStreamWriter struct {
	mu     sync.Mutex
	header http.Header
	status int
	body   strings.Builder
	wake   chan struct{}
}

func newIntegrationStreamWriter() *integrationStreamWriter {
	return &integrationStreamWriter{header: make(http.Header), wake: make(chan struct{}, 1)}
}

func (w *integrationStreamWriter) Header() http.Header { return w.header }
func (w *integrationStreamWriter) WriteHeader(status int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.status = status
}
func (w *integrationStreamWriter) Write(value []byte) (int, error) {
	w.mu.Lock()
	n, err := w.body.Write(value)
	w.mu.Unlock()
	select {
	case w.wake <- struct{}{}:
	default:
	}
	return n, err
}
func (w *integrationStreamWriter) Flush() {}
func (w *integrationStreamWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.body.String()
}
func (w *integrationStreamWriter) waitFor(needles ...string) error {
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for {
		body := w.String()
		allFound := true
		for _, needle := range needles {
			allFound = allFound && strings.Contains(body, needle)
		}
		if allFound {
			return nil
		}
		select {
		case <-w.wake:
		case <-deadline.C:
			return errors.New("timed out waiting for expected SSE events")
		}
	}
}

func serveTask6LifecycleAppServer(t *testing.T, conn net.Conn) error {
	t.Helper()
	defer conn.Close()
	initializeFakeAppServer(t, conn)
	threads := make(map[string]map[string]any)
	startThread := func(expectedProvider, expectedModel, expectedEffort string, number int) (string, error) {
		request := readAppServerRequest(t, conn)
		if request.Method != "thread/start" || request.Params["modelProvider"] != expectedProvider || request.Params["model"] != expectedModel {
			return "", errors.New("new thread did not use the expected pinned provider/model")
		}
		config, _ := request.Params["config"].(map[string]any)
		if config["model_reasoning_effort"] != expectedEffort {
			return "", errors.New("new thread did not use the expected pinned reasoning effort")
		}
		id := "thread-" + string(rune('0'+number))
		thread := map[string]any{"id": id, "modelProvider": expectedProvider, "model": expectedModel, "reasoningEffort": expectedEffort, "threadSource": sessionUIServiceName}
		threads[id] = thread
		writeAppServerResponse(t, conn, request.ID, map[string]any{"thread": thread, "modelProvider": expectedProvider, "model": expectedModel, "reasoningEffort": expectedEffort, "approvalPolicy": "on-request"})
		return id, nil
	}
	startTurn := func(threadID, turnID string) error {
		request := readAppServerRequest(t, conn)
		if request.Method != "turn/start" || request.Params["threadId"] != threadID {
			return errors.New("turn did not continue on the expected thread")
		}
		writeAppServerResponse(t, conn, request.ID, map[string]any{"turn": map[string]any{"id": turnID}})
		return nil
	}
	firstID, err := startThread("openai", "gpt-6-luna", "high", 1)
	if err != nil {
		return err
	}
	if err := startTurn(firstID, "turn-1"); err != nil {
		return err
	}
	if _, err := conn.Write([]byte(`{"method":"item/agentMessage/delta","params":{"threadId":"thread-1","turnId":"turn-1","itemId":"item-1","delta":"starting safely"}}` + "\n")); err != nil {
		return err
	}
	if _, err := conn.Write([]byte(`{"id":"approval-1","method":"item/commandExecution/requestApproval","params":{"threadId":"thread-1","turnId":"turn-1","itemId":"item-approval","command":"printf safe","cwd":"C:/repo","availableDecisions":["accept","decline"]}}` + "\n")); err != nil {
		return err
	}
	approvalLine, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		return err
	}
	var approvalResponse struct {
		ID     json.RawMessage   `json:"id"`
		Result map[string]string `json:"result"`
	}
	if err := json.Unmarshal(approvalLine, &approvalResponse); err != nil || string(approvalResponse.ID) != `"approval-1"` || approvalResponse.Result["decision"] != "accept" {
		return errors.New("App Server did not receive the explicit one-shot approval")
	}
	if _, err := conn.Write([]byte(`{"method":"turn/completed","params":{"threadId":"thread-1","turn":{"id":"turn-1","status":"completed"}}}` + "\n")); err != nil {
		return err
	}
	if err := startTurn(firstID, "turn-2"); err != nil {
		return err
	}
	if _, err := conn.Write([]byte(`{"method":"item/agentMessage/delta","params":{"threadId":"thread-1","turnId":"turn-2","itemId":"item-2","delta":"continued on the same thread"}}` + "\n")); err != nil {
		return err
	}
	if _, err := conn.Write([]byte(`{"method":"turn/completed","params":{"threadId":"thread-1","turn":{"id":"turn-2","status":"completed"}}}` + "\n")); err != nil {
		return err
	}
	fallbackID, err := startThread("osc", "muse-spark-1.3-contributor", "xhigh", 2)
	if err != nil {
		return err
	}
	if err := startTurn(fallbackID, "turn-3"); err != nil {
		return err
	}
	if _, err := conn.Write([]byte(`{"method":"turn/completed","params":{"threadId":"thread-2","turn":{"id":"turn-3","status":"completed"}}}` + "\n")); err != nil {
		return err
	}
	recoveredID, err := startThread("openai", "gpt-6-luna", "high", 3)
	if err != nil {
		return err
	}
	if err := startTurn(recoveredID, "turn-4"); err != nil {
		return err
	}
	if _, err := conn.Write([]byte(`{"method":"turn/completed","params":{"threadId":"thread-3","turn":{"id":"turn-4","status":"completed"}}}` + "\n")); err != nil {
		return err
	}
	resume := readAppServerRequest(t, conn)
	if resume.Method != "thread/resume" || resume.Params["threadId"] != firstID {
		return errors.New("existing thread did not resume by its pinned identity")
	}
	if _, exists := resume.Params["modelProvider"]; exists {
		return errors.New("resume attempted to change the pinned provider")
	}
	thread := threads[firstID]
	writeAppServerResponse(t, conn, resume.ID, map[string]any{"thread": thread, "modelProvider": "openai", "model": "gpt-6-luna", "reasoningEffort": "high", "approvalPolicy": "on-request"})
	return nil
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
