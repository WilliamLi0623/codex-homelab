package codexsession

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/WilliamLi0623/codex-homelab/internal/codexrouting"
)

type testRouteCoordinator struct {
	mu        sync.Mutex
	decision  codexrouting.RouteDecision
	refreshes int
	err       error
	started   chan struct{}
	block     bool
}

func (c *testRouteCoordinator) CurrentDecision(context.Context) (codexrouting.RouteDecision, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.decision, c.err
}

func (c *testRouteCoordinator) RefreshAndDecide(ctx context.Context) (codexrouting.RouteDecision, error) {
	c.mu.Lock()
	c.refreshes++
	decision, err, started, block := c.decision, c.err, c.started, c.block
	c.mu.Unlock()
	if started != nil {
		select {
		case started <- struct{}{}:
		default:
		}
	}
	if block {
		<-ctx.Done()
		return codexrouting.RouteDecision{}, ctx.Err()
	}
	return decision, err
}

type testSessionBackend struct {
	mu          sync.Mutex
	sessions    map[string]*ManagedSession
	created     int
	turns       int
	interrupted int
	approvals   int
	createErr   error
	routes      []RoutePin
	options     []ThreadStartOptions
}

func (b *testSessionBackend) CreateThread(_ context.Context, options ThreadStartOptions, pin RoutePin) (*ManagedSession, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.createErr != nil {
		return nil, b.createErr
	}
	b.created++
	b.routes = append(b.routes, pin)
	b.options = append(b.options, options)
	id := "thread-" + string(rune('0'+b.created))
	thread := AppServerThread{ID: id, ModelProvider: options.ModelProvider, Model: options.Model, ReasoningEffort: stringPointer(options.Effort), ThreadSource: stringPointer(sessionUIServiceName)}
	session := &ManagedSession{Thread: thread, Pin: pin, Notices: make(chan SessionNotice, 4)}
	if b.sessions == nil {
		b.sessions = make(map[string]*ManagedSession)
	}
	b.sessions[id] = session
	return session, nil
}

func (b *testSessionBackend) Get(id string) *ManagedSession {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sessions[id]
}

func (b *testSessionBackend) List() []*ManagedSession {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]*ManagedSession, 0, len(b.sessions))
	for _, session := range b.sessions {
		out = append(out, session)
	}
	return out
}

func (b *testSessionBackend) StartTurn(context.Context, string, string) (Turn, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.turns++
	return Turn{ID: "turn"}, nil
}

func (b *testSessionBackend) InterruptTurn(context.Context, string, string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.interrupted++
	return nil
}

func (b *testSessionBackend) RespondApprovalID(context.Context, string, string, string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.approvals++
	return nil
}

func testHTTPHandler(t *testing.T, decision codexrouting.RouteDecision, fallbackEnabled bool) (http.Handler, *testRouteCoordinator, *testSessionBackend) {
	t.Helper()
	coordinator := &testRouteCoordinator{decision: decision}
	backend := &testSessionBackend{sessions: make(map[string]*ManagedSession)}
	handler, err := NewSessionHTTPHandler(SessionHTTPConfig{
		ListenAddress:   "127.0.0.1:8765",
		Origin:          "http://127.0.0.1:8765",
		Token:           strings.Repeat("t", 32),
		StaticFiles:     fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<!doctype html><title>Local UI</title>")}},
		MaxSSE:          2,
		FallbackEnabled: fallbackEnabled,
	}, coordinator, backend)
	if err != nil {
		t.Fatal(err)
	}
	return handler, coordinator, backend
}

func localUICookie(t *testing.T, handler http.Handler) *http.Cookie {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8765/", nil)
	request.Host = "127.0.0.1:8765"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == sessionUICookieName {
			return cookie
		}
	}
	t.Fatalf("root did not set session auth cookie: %v", response.Header())
	return nil
}

func authorizedUIRequest(handler http.Handler, method, path string, body io.Reader, cookie *http.Cookie) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, "http://127.0.0.1:8765"+path, body)
	request.Host = "127.0.0.1:8765"
	request.AddCookie(cookie)
	if method != http.MethodGet && method != http.MethodHead {
		request.Header.Set("Origin", "http://127.0.0.1:8765")
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func sessionCreateBody(t *testing.T, prompt string) string {
	t.Helper()
	cwd, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(map[string]string{"cwd": cwd, "prompt": prompt})
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func TestSessionHTTPRejectsWildcardAndNonLoopbackListeners(t *testing.T) {
	for _, address := range []string{"0.0.0.0:8765", "[::]:8765", "100.64.1.5:8765", "localhost:8765", "127.0.0.1:0"} {
		t.Run(address, func(t *testing.T) {
			_, err := NewSessionHTTPHandler(SessionHTTPConfig{ListenAddress: address, Origin: "http://127.0.0.1:8765", Token: strings.Repeat("x", 32)}, &testRouteCoordinator{}, &testSessionBackend{})
			if err == nil {
				t.Fatalf("unsafe listener %q accepted", address)
			}
		})
	}
}

func TestSessionHTTPRequiresExactHostCookieAndOriginForWrites(t *testing.T) {
	handler, coordinator, backend := testHTTPHandler(t, codexrouting.RouteDecision{Mode: codexrouting.ModeNormal, CanStart: true, Published: true}, false)
	cookie := localUICookie(t, handler)

	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8765/api/sessions", strings.NewReader(sessionCreateBody(t, "hello")))
	request.Host = "evil.example"
	request.AddCookie(cookie)
	request.Header.Set("Origin", "http://127.0.0.1:8765")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("wrong Host status=%d body=%s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8765/api/sessions", strings.NewReader(sessionCreateBody(t, "hello")))
	request.Host = "127.0.0.1:8765"
	request.AddCookie(cookie)
	request.Header.Set("Origin", "http://evil.example")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("wrong Origin status=%d body=%s", response.Code, response.Body.String())
	}
	if coordinator.refreshes != 0 || backend.created != 0 {
		t.Fatalf("rejected request reached coordinator/backend: refresh=%d created=%d", coordinator.refreshes, backend.created)
	}

	request = httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8765/api/sessions", strings.NewReader(sessionCreateBody(t, "hello")))
	request.Host = "127.0.0.1:8765"
	request.Header.Set("Origin", "http://127.0.0.1:8765")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("missing cookie status=%d", response.Code)
	}
}

func TestSessionHTTPCreateUsesOnlyNormalRouteAndPinsInitialTurn(t *testing.T) {
	decision := codexrouting.RouteDecision{Mode: codexrouting.ModeNormal, ObservedMode: codexrouting.ModeNormal, Generation: 9, Fresh: true, Published: true, CanStart: true}
	handler, coordinator, backend := testHTTPHandler(t, decision, false)
	cookie := localUICookie(t, handler)
	response := authorizedUIRequest(handler, http.MethodPost, "/api/sessions", strings.NewReader(sessionCreateBody(t, "say hello")), cookie)
	if response.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", response.Code, response.Body.String())
	}
	if coordinator.refreshes != 1 || backend.created != 1 || backend.turns != 1 {
		t.Fatalf("refresh/create/turn=%d/%d/%d", coordinator.refreshes, backend.created, backend.turns)
	}
	var result struct {
		Session struct {
			ID       string `json:"id"`
			Provider string `json:"provider"`
			Model    string `json:"model"`
			Effort   string `json:"effort"`
		} `json:"session"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Session.ID != "thread-1" || result.Session.Provider != "openai" || result.Session.Model != "gpt-6-luna" || result.Session.Effort != "high" {
		t.Fatalf("created route=%+v", result.Session)
	}
	if len(backend.options) != 1 || backend.options[0].Config != nil || backend.options[0].ModelProvider != "openai" {
		t.Fatalf("OpenAI route was not direct: %+v", backend.options)
	}
}

func TestSessionHTTPQuotaTransitionAffectsOnlyNewThreads(t *testing.T) {
	decision := codexrouting.RouteDecision{Mode: codexrouting.ModeNormal, Generation: 1, Fresh: true, Published: true, CanStart: true}
	handler, coordinator, backend := testHTTPHandler(t, decision, true)
	cookie := localUICookie(t, handler)
	first := authorizedUIRequest(handler, http.MethodPost, "/api/sessions", strings.NewReader(sessionCreateBody(t, "first")), cookie)
	if first.Code != http.StatusCreated {
		t.Fatalf("normal create status=%d: %s", first.Code, first.Body.String())
	}
	coordinator.mu.Lock()
	coordinator.decision = codexrouting.RouteDecision{Mode: codexrouting.ModeQuotaFallback, Generation: 2, Fresh: true, Published: true, CanStart: true}
	coordinator.mu.Unlock()
	second := authorizedUIRequest(handler, http.MethodPost, "/api/sessions", strings.NewReader(sessionCreateBody(t, "second")), cookie)
	if second.Code != http.StatusCreated {
		t.Fatalf("fallback create status=%d: %s", second.Code, second.Body.String())
	}
	if backend.created != 2 || backend.routes[0].Provider != "openai" || backend.routes[0].Model != "gpt-6-luna" || backend.routes[1].Provider != "osc" || backend.routes[1].Model != "muse-spark-1.3-contributor" {
		t.Fatalf("new-thread routes=%+v", backend.routes)
	}
	if old := backend.Get("thread-1"); old == nil || old.Pin.Provider != "openai" || old.Pin.Generation != 1 {
		t.Fatalf("existing thread route was mutated: %+v", old)
	}
}

func TestSessionHTTPRoutePublicationErrorBlocksThreadCreation(t *testing.T) {
	handler, coordinator, backend := testHTTPHandler(t, codexrouting.RouteDecision{Mode: codexrouting.ModeQuotaFallback, CanStart: false}, false)
	coordinator.err = errors.New("controller secret must not be returned")
	cookie := localUICookie(t, handler)
	response := authorizedUIRequest(handler, http.MethodPost, "/api/sessions", strings.NewReader(sessionCreateBody(t, "hello")), cookie)
	if response.Code != http.StatusServiceUnavailable || backend.created != 0 || strings.Contains(response.Body.String(), "controller secret") {
		t.Fatalf("route publication failure status=%d created=%d body=%s", response.Code, backend.created, response.Body.String())
	}
}

func TestSessionHTTPBlocksFallbackUntilSubagentRouteGateIsProven(t *testing.T) {
	decision := codexrouting.RouteDecision{Mode: codexrouting.ModeQuotaFallback, ObservedMode: codexrouting.ModeQuotaFallback, Generation: 10, Fresh: true, Published: true, CanStart: true}
	handler, coordinator, backend := testHTTPHandler(t, decision, false)
	cookie := localUICookie(t, handler)
	response := authorizedUIRequest(handler, http.MethodPost, "/api/sessions", strings.NewReader(sessionCreateBody(t, "hello")), cookie)
	if response.Code != http.StatusServiceUnavailable || backend.created != 0 || coordinator.refreshes != 1 {
		t.Fatalf("fallback gate status=%d created=%d refreshes=%d body=%s", response.Code, backend.created, coordinator.refreshes, response.Body.String())
	}
}

func TestSessionHTTPRejectsUnavailableRouteWithoutCreatingThread(t *testing.T) {
	decision := codexrouting.RouteDecision{Mode: codexrouting.ModeQuotaFallback, Generation: 4, CanStart: false, Published: false}
	handler, coordinator, backend := testHTTPHandler(t, decision, true)
	cookie := localUICookie(t, handler)
	response := authorizedUIRequest(handler, http.MethodPost, "/api/sessions", strings.NewReader(sessionCreateBody(t, "hello")), cookie)
	if response.Code != http.StatusServiceUnavailable || coordinator.refreshes != 1 || backend.created != 0 {
		t.Fatalf("unpublished route status=%d created=%d refresh=%d", response.Code, backend.created, coordinator.refreshes)
	}
}

func TestSessionHTTPBoundsBodyAndRedactsToken(t *testing.T) {
	decision := codexrouting.RouteDecision{Mode: codexrouting.ModeNormal, CanStart: true, Published: true}
	handler, coordinator, backend := testHTTPHandler(t, decision, false)
	cookie := localUICookie(t, handler)
	body := sessionCreateBody(t, strings.Repeat("x", 140<<10))
	response := authorizedUIRequest(handler, http.MethodPost, "/api/sessions", strings.NewReader(body), cookie)
	if response.Code != http.StatusRequestEntityTooLarge || coordinator.refreshes != 0 || backend.created != 0 {
		t.Fatalf("oversized body status=%d refresh=%d created=%d", response.Code, coordinator.refreshes, backend.created)
	}
	if strings.Contains(response.Body.String(), strings.Repeat("t", 32)) || strings.Contains(response.Header().Get("Set-Cookie"), strings.Repeat("t", 32)) {
		t.Fatal("local API credential leaked in response")
	}
}

func TestSessionHTTPUnknownRoutesAreNotProxied(t *testing.T) {
	handler, _, _ := testHTTPHandler(t, codexrouting.RouteDecision{}, false)
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8765/api/admin", nil)
	request.Host = "127.0.0.1:8765"
	request.AddCookie(localUICookie(t, handler))
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("unknown API route status=%d", response.Code)
	}
}

func TestSessionHTTPSSEFiltersHiddenReasoningAndBoundsConnections(t *testing.T) {
	decision := codexrouting.RouteDecision{Mode: codexrouting.ModeNormal, CanStart: true, Published: true}
	handler, _, backend := testHTTPHandler(t, decision, false)
	cookie := localUICookie(t, handler)
	session := &ManagedSession{Thread: AppServerThread{ID: "thread-1"}, Pin: RoutePin{Mode: "normal"}, Notices: make(chan SessionNotice, 4)}
	backend.sessions["thread-1"] = session
	session.Notices <- SessionNotice{Event: &Event{Method: "item/reasoning/summaryTextDelta", Params: json.RawMessage(`{"threadId":"thread-1","delta":"private thought"}`)}}
	session.Notices <- SessionNotice{Event: &Event{Method: "item/agentMessage/delta", Params: json.RawMessage(`{"threadId":"thread-1","turnId":"turn-1","itemId":"item-1","delta":"visible"}`)}}
	close(session.Notices)
	response := authorizedUIRequest(handler, http.MethodGet, "/api/sessions/thread-1/events", nil, cookie)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "visible") || strings.Contains(response.Body.String(), "private thought") {
		t.Fatalf("filtered SSE status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestSessionHTTPRouteTableIsExplicit(t *testing.T) {
	normal, err := SessionRouteForMode(codexrouting.ModeNormal)
	if err != nil || normal.Provider != "openai" || normal.Model != "gpt-6-luna" || normal.Effort != "high" {
		t.Fatalf("normal route=%+v err=%v", normal, err)
	}
	fallback, err := SessionRouteForMode(codexrouting.ModeQuotaFallback)
	if err != nil || fallback.Provider != "osc" || fallback.Model != "muse-spark-1.3-contributor" || fallback.Effort != "xhigh" {
		t.Fatalf("fallback route=%+v err=%v", fallback, err)
	}
	if _, err := SessionRouteForMode(codexrouting.ModeUnknown); err == nil {
		t.Fatal("unknown route must not be inferred")
	}
}

func TestSessionHTTPHostAndOriginMismatchOnStatusIsRejected(t *testing.T) {
	handler, _, _ := testHTTPHandler(t, codexrouting.RouteDecision{}, false)
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8765/api/status", nil)
	request.Host = "localhost:8765"
	request.AddCookie(localUICookie(t, handler))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("unexpected host accepted: %d", response.Code)
	}
}

func TestSessionHTTPContextCancellationDoesNotStartTurn(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	handler, _, backend := testHTTPHandler(t, codexrouting.RouteDecision{Mode: codexrouting.ModeNormal, CanStart: true, Published: true}, false)
	cookie := localUICookie(t, handler)
	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8765/api/sessions", strings.NewReader(sessionCreateBody(t, "hello"))).WithContext(ctx)
	request.Host = "127.0.0.1:8765"
	request.AddCookie(cookie)
	request.Header.Set("Origin", "http://127.0.0.1:8765")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if backend.created != 0 {
		t.Fatalf("cancelled request created %d sessions", backend.created)
	}
}

func TestSessionHTTPConcurrentNewSessionsEachUseFreshCoordinatorDecision(t *testing.T) {
	decision := codexrouting.RouteDecision{Mode: codexrouting.ModeNormal, Generation: 2, Fresh: true, Published: true, CanStart: true}
	handler, coordinator, backend := testHTTPHandler(t, decision, false)
	cookie := localUICookie(t, handler)
	var wait sync.WaitGroup
	errs := make(chan string, 2)
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			response := authorizedUIRequest(handler, http.MethodPost, "/api/sessions", strings.NewReader(sessionCreateBody(t, "hello")), cookie)
			if response.Code != http.StatusCreated {
				errs <- response.Body.String()
			}
		}()
	}
	wait.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if coordinator.refreshes != 2 || backend.created != 2 || backend.turns != 2 {
		t.Fatalf("parallel creates refresh/create/turn=%d/%d/%d", coordinator.refreshes, backend.created, backend.turns)
	}
}

func TestSessionHTTPQuotaRefreshCancellationPreventsCreation(t *testing.T) {
	handler, coordinator, backend := testHTTPHandler(t, codexrouting.RouteDecision{Mode: codexrouting.ModeNormal, CanStart: true, Published: true}, false)
	coordinator.started = make(chan struct{}, 1)
	coordinator.block = true
	cookie := localUICookie(t, handler)
	ctx, cancel := context.WithCancel(context.Background())
	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8765/api/sessions", strings.NewReader(sessionCreateBody(t, "hello"))).WithContext(ctx)
	request.Host = "127.0.0.1:8765"
	request.AddCookie(cookie)
	request.Header.Set("Origin", "http://127.0.0.1:8765")
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { handler.ServeHTTP(response, request); close(done) }()
	<-coordinator.started
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancelled route refresh did not return")
	}
	if backend.created != 0 || response.Code != http.StatusServiceUnavailable {
		t.Fatalf("cancelled refresh created=%d status=%d", backend.created, response.Code)
	}
}

func TestSessionHTTPEnforcesSSEConnectionLimit(t *testing.T) {
	handler, _, backend := testHTTPHandler(t, codexrouting.RouteDecision{}, false)
	server := handler.(*SessionHTTPHandler)
	cookie := localUICookie(t, handler)
	for _, id := range []string{"one", "two"} {
		backend.sessions[id] = &ManagedSession{Thread: AppServerThread{ID: id}, Notices: make(chan SessionNotice)}
	}
	type stream struct {
		cancel context.CancelFunc
		done   chan struct{}
	}
	streams := make([]stream, 2)
	for i, id := range []string{"one", "two"} {
		ctx, cancel := context.WithCancel(context.Background())
		streams[i] = stream{cancel: cancel, done: make(chan struct{})}
		request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8765/api/sessions/"+id+"/events", nil).WithContext(ctx)
		request.Host = "127.0.0.1:8765"
		request.AddCookie(cookie)
		go func(done chan struct{}) { defer close(done); handler.ServeHTTP(httptest.NewRecorder(), request) }(streams[i].done)
	}
	deadline := time.After(time.Second)
	for {
		server.sseMu.Lock()
		active := server.sseActive
		server.sseMu.Unlock()
		if active == 2 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("active SSE streams=%d, want 2", active)
		case <-time.After(5 * time.Millisecond):
		}
	}
	response := authorizedUIRequest(handler, http.MethodGet, "/api/sessions/one/events", nil, cookie)
	if response.Code != http.StatusTooManyRequests {
		t.Fatalf("third SSE status=%d", response.Code)
	}
	for _, current := range streams {
		current.cancel()
		<-current.done
	}
}

func TestSessionHTTPAllowsOnlyOneSSEConsumerPerThread(t *testing.T) {
	handler, _, backend := testHTTPHandler(t, codexrouting.RouteDecision{}, false)
	server := handler.(*SessionHTTPHandler)
	cookie := localUICookie(t, handler)
	backend.sessions["thread-one"] = &ManagedSession{Thread: AppServerThread{ID: "thread-one"}, Notices: make(chan SessionNotice)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8765/api/sessions/thread-one/events", nil).WithContext(ctx)
	request.Host = "127.0.0.1:8765"
	request.AddCookie(cookie)
	done := make(chan struct{})
	go func() { handler.ServeHTTP(httptest.NewRecorder(), request); close(done) }()
	deadline := time.After(time.Second)
	for {
		server.sseMu.Lock()
		active := server.sseByThread["thread-one"]
		server.sseMu.Unlock()
		if active == 1 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("first session event stream did not start")
		case <-time.After(5 * time.Millisecond):
		}
	}
	response := authorizedUIRequest(handler, http.MethodGet, "/api/sessions/thread-one/events", nil, cookie)
	if response.Code != http.StatusTooManyRequests {
		t.Fatalf("second SSE consumer status=%d", response.Code)
	}
	cancel()
	<-done
}

func TestCookieIsHttpOnlyStrictAndNotEchoed(t *testing.T) {
	handler, _, _ := testHTTPHandler(t, codexrouting.RouteDecision{}, false)
	cookie := localUICookie(t, handler)
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || len(cookie.Value) < 32 {
		t.Fatalf("unsafe bootstrap cookie=%+v", cookie)
	}
}

func TestSessionHTTPStaticPageCarriesSecurityHeadersAndOmitsTokenBody(t *testing.T) {
	handler, _, _ := testHTTPHandler(t, codexrouting.RouteDecision{}, false)
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8765/", nil)
	request.Host = "127.0.0.1:8765"
	handler.ServeHTTP(response, request)
	for name, want := range map[string]string{
		"Content-Security-Policy": "default-src 'self'",
		"X-Content-Type-Options":  "nosniff",
		"Referrer-Policy":         "no-referrer",
		"Cache-Control":           "no-store",
	} {
		if !strings.Contains(response.Header().Get(name), want) {
			t.Fatalf("header %s=%q, want to contain %q", name, response.Header().Get(name), want)
		}
	}
	if strings.Contains(response.Body.String(), strings.Repeat("t", 32)) {
		t.Fatal("local auth token was included in the static response body")
	}
}
