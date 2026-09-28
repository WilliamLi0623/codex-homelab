package codexrouting

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type testStore struct {
	mu    sync.Mutex
	state RoutingState
	ok    bool
	err   error
	saves []RoutingState
}

func (s *testStore) Load(context.Context) (RoutingState, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state, s.ok, s.err
}
func (s *testStore) Save(_ context.Context, state RoutingState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	s.state, s.ok = state, true
	s.saves = append(s.saves, state)
	return nil
}

type testSink struct {
	mu      sync.Mutex
	calls   []RoutingState
	states  []RoutingState
	err     error
	started chan struct{}
	release chan struct{}
}

func (s *testSink) Publish(ctx context.Context, state RoutingState) error {
	s.mu.Lock()
	s.calls = append(s.calls, state)
	s.mu.Unlock()
	if s.started != nil {
		select {
		case s.started <- struct{}{}:
		default:
		}
	}
	if s.release != nil {
		select {
		case <-s.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	s.states = append(s.states, state)
	return nil
}

type testReader struct {
	mu      sync.Mutex
	snaps   []QuotaSnapshot
	err     error
	reads   int
	started chan struct{}
	release chan struct{}
}

func (r *testReader) Read(ctx context.Context) (QuotaSnapshot, error) {
	if r.started != nil {
		select {
		case r.started <- struct{}{}:
		default:
		}
	}
	if r.release != nil {
		select {
		case <-r.release:
		case <-ctx.Done():
			return QuotaSnapshot{}, ctx.Err()
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reads++
	if r.err != nil {
		return QuotaSnapshot{}, r.err
	}
	if len(r.snaps) == 0 {
		return QuotaSnapshot{}, errors.New("no snapshot")
	}
	snapshot := r.snaps[0]
	r.snaps = r.snaps[1:]
	return snapshot, nil
}

func quotaAllowed(value bool) *bool { return &value }

func newCoordinatorForTest(t *testing.T, reader QuotaReader, sink ModeSink, store RoutingStateStore) *Coordinator {
	t.Helper()
	c, err := NewCoordinator(reader, sink, store, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	c.Now = func() time.Time { return time.Date(2026, 9, 23, 12, 0, 0, 123456789, time.UTC) }
	return c
}

func TestCoordinatorPublishesTransitionsAndPersistsGeneration(t *testing.T) {
	reader := &testReader{snaps: []QuotaSnapshot{{OrdinaryUsageAllowed: quotaAllowed(true)}, {OrdinaryUsageAllowed: quotaAllowed(true)}, {OrdinaryUsageAllowed: quotaAllowed(false)}, {RateLimitReachedType: "unknown"}}}
	sink := &testSink{}
	store := &testStore{}
	c := newCoordinatorForTest(t, reader, sink, store)
	for i := 0; i < 4; i++ {
		if err := c.PollOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if len(sink.states) != 2 || len(store.saves) != 2 {
		t.Fatalf("published=%d saved=%d, want 2/2", len(sink.states), len(store.saves))
	}
	if sink.states[0].Mode != ModeNormal || sink.states[0].Generation != 1 {
		t.Fatalf("first state=%+v", sink.states[0])
	}
	if sink.states[1].Mode != ModeQuotaFallback || sink.states[1].Generation != 2 {
		t.Fatalf("second state=%+v", sink.states[1])
	}
}

func TestCoordinatorRefreshAndDecideReturnsPublishedFreshRoute(t *testing.T) {
	reader := &testReader{snaps: []QuotaSnapshot{{OrdinaryUsageAllowed: quotaAllowed(false)}}}
	sink := &testSink{}
	store := &testStore{}
	c := newCoordinatorForTest(t, reader, sink, store)

	decision, err := c.RefreshAndDecide(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if decision.Mode != ModeQuotaFallback || decision.ObservedMode != ModeQuotaFallback {
		t.Fatalf("decision modes = %q/%q, want quota_fallback/quota_fallback", decision.Mode, decision.ObservedMode)
	}
	if !decision.Fresh || !decision.Published {
		t.Fatalf("decision freshness/publication = %t/%t, want true/true", decision.Fresh, decision.Published)
	}
	if decision.Generation != 1 || !decision.ObservedAt.Equal(c.Now()) {
		t.Fatalf("decision generation/time = %d/%s, want 1/%s", decision.Generation, decision.ObservedAt, c.Now())
	}
	if len(store.saves) != 1 || len(sink.states) != 1 || store.saves[0] != sink.states[0] {
		t.Fatalf("persisted/published state mismatch: saved=%+v published=%+v", store.saves, sink.states)
	}
}

func TestCoordinatorRefreshAndDecideUsesConservativeNormalWhenQuotaUnknownWithoutState(t *testing.T) {
	c := newCoordinatorForTest(t, &testReader{snaps: []QuotaSnapshot{{RateLimitReachedType: "unrecognized"}}}, &testSink{}, &testStore{})

	decision, err := c.RefreshAndDecide(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if decision.Mode != ModeNormal || decision.ObservedMode != ModeUnknown {
		t.Fatalf("decision modes = %q/%q, want normal/unknown", decision.Mode, decision.ObservedMode)
	}
	if decision.Fresh || decision.Published || decision.Generation != 0 || !decision.ObservedAt.IsZero() {
		t.Fatalf("unknown decision claims an observation or publication: %+v", decision)
	}
}

func TestCoordinatorRefreshAndDecidePreservesLastPublishedModeWhenQuotaUnknown(t *testing.T) {
	reader := &testReader{snaps: []QuotaSnapshot{{OrdinaryUsageAllowed: quotaAllowed(false)}, {RateLimitReachedType: "unrecognized"}}}
	sink := &testSink{}
	c := newCoordinatorForTest(t, reader, sink, &testStore{})
	if _, err := c.RefreshAndDecide(context.Background()); err != nil {
		t.Fatal(err)
	}

	decision, err := c.RefreshAndDecide(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if decision.Mode != ModeQuotaFallback || decision.ObservedMode != ModeUnknown || decision.Fresh || !decision.Published {
		t.Fatalf("unknown observation did not preserve published route: %+v", decision)
	}
	if decision.Generation != 1 || len(sink.states) != 1 {
		t.Fatalf("unknown observation changed generation/publication: decision=%+v published=%+v", decision, sink.states)
	}
}

func TestCoordinatorRefreshAndDecideDoesNotReturnUsableRouteWhenPublicationFails(t *testing.T) {
	sink := &testSink{err: errors.New("controller unavailable")}
	c := newCoordinatorForTest(t, &testReader{snaps: []QuotaSnapshot{{OrdinaryUsageAllowed: quotaAllowed(false)}}}, sink, &testStore{})

	decision, err := c.RefreshAndDecide(context.Background())
	if err == nil {
		t.Fatal("expected publication failure")
	}
	if decision.Published {
		t.Fatalf("failed publication yielded a published route: %+v", decision)
	}
	if len(sink.calls) != 1 || sink.calls[0].Generation != 1 {
		t.Fatalf("expected one attempted publication, got %+v", sink.calls)
	}
}

func TestCoordinatorRefreshAndDecideSharesPollSerializationGate(t *testing.T) {
	reader := &testReader{snaps: []QuotaSnapshot{{OrdinaryUsageAllowed: quotaAllowed(true)}}, started: make(chan struct{}, 1), release: make(chan struct{})}
	c := newCoordinatorForTest(t, reader, &testSink{}, &testStore{})
	firstDone := make(chan error, 1)
	go func() {
		_, err := c.RefreshAndDecide(context.Background())
		firstDone <- err
	}()
	<-reader.started
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.RefreshAndDecide(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("second refresh error=%v, want context cancellation", err)
	}
	close(reader.release)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
}

func TestCoordinatorRefreshAndDecidePropagatesCallerCancellationDuringQuotaRead(t *testing.T) {
	reader := &testReader{started: make(chan struct{}, 1), release: make(chan struct{})}
	c := newCoordinatorForTest(t, reader, &testSink{}, &testStore{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct {
		decision RouteDecision
		err      error
	}, 1)
	go func() {
		decision, err := c.RefreshAndDecide(ctx)
		done <- struct {
			decision RouteDecision
			err      error
		}{decision, err}
	}()
	<-reader.started
	cancel()
	result := <-done
	if !errors.Is(result.err, context.Canceled) {
		t.Fatalf("refresh error=%v, want caller cancellation", result.err)
	}
	if result.decision.CanStart {
		t.Fatalf("caller cancellation returned a usable route: %+v", result.decision)
	}
}

func TestCurrentDecisionIsConservativeWithoutPersistedState(t *testing.T) {
	c := newCoordinatorForTest(t, &testReader{}, &testSink{}, &testStore{})
	decision, err := c.CurrentDecision(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if decision.Mode != ModeNormal || decision.ObservedMode != ModeUnknown || decision.Generation != 0 || decision.Fresh || !decision.CanStart {
		t.Fatalf("initial current decision=%+v", decision)
	}
}

func TestCurrentDecisionBlocksUnpublishedPersistedState(t *testing.T) {
	state := RoutingState{Mode: ModeQuotaFallback, ObservedAt: time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC), Generation: 3}
	c := newCoordinatorForTest(t, &testReader{}, &testSink{}, &testStore{state: state, ok: true})
	decision, err := c.CurrentDecision(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if decision.Mode != ModeQuotaFallback || decision.Generation != 3 || decision.Published || decision.CanStart {
		t.Fatalf("persisted but unpublished decision=%+v", decision)
	}
}

func TestCoordinatorRestartReadsCurrentQuotaBeforePublishing(t *testing.T) {
	state := RoutingState{Mode: ModeQuotaFallback, Generation: 7, ObservedAt: time.Date(2026, 9, 23, 1, 2, 3, 4, time.UTC)}
	store := &testStore{state: state, ok: true}
	sink := &testSink{}
	reader := &testReader{snaps: []QuotaSnapshot{{OrdinaryUsageAllowed: quotaAllowed(true)}}}
	c := newCoordinatorForTest(t, reader, sink, store)
	if err := c.PollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := RoutingState{Mode: ModeNormal, Generation: 8, ObservedAt: c.Now()}
	if len(sink.states) != 1 || sink.states[0] != want {
		t.Fatalf("states=%+v, want fresh authoritative state %+v", sink.states, want)
	}
	if len(store.saves) != 1 || store.saves[0] != want {
		t.Fatalf("persisted=%+v, want fresh authoritative state %+v", store.saves, want)
	}
}

func TestCoordinatorRestartDoesNotPublishDurableStateWhenQuotaIsUnknown(t *testing.T) {
	state := RoutingState{Mode: ModeQuotaFallback, Generation: 7, ObservedAt: time.Date(2026, 9, 23, 1, 2, 3, 4, time.UTC)}
	store := &testStore{state: state, ok: true}
	sink := &testSink{}
	reader := &testReader{snaps: []QuotaSnapshot{{RateLimitReachedType: "unrecognized"}}}
	c := newCoordinatorForTest(t, reader, sink, store)
	if err := c.PollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(sink.states) != 0 || len(store.saves) != 0 {
		t.Fatalf("unknown quota observation published or changed stale state: published=%+v saved=%+v", sink.states, store.saves)
	}
}

func TestCoordinatorRestartRepublishesPendingStateOnlyAfterMatchingQuotaRead(t *testing.T) {
	state := RoutingState{Mode: ModeQuotaFallback, Generation: 7, ObservedAt: time.Date(2026, 9, 23, 1, 2, 3, 4, time.UTC)}
	store := &testStore{state: state, ok: true}
	sink := &testSink{}
	reader := &testReader{snaps: []QuotaSnapshot{{OrdinaryUsageAllowed: quotaAllowed(false)}}}
	c := newCoordinatorForTest(t, reader, sink, store)
	if err := c.PollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(sink.states) != 1 || sink.states[0] != state {
		t.Fatalf("states=%+v, want exact pending durable state after matching authoritative read", sink.states)
	}
	if len(store.saves) != 0 {
		t.Fatalf("republishing persisted state unexpectedly changed durable state: %+v", store.saves)
	}
}

func TestCoordinatorReadAndSinkErrorsPreserveState(t *testing.T) {
	state := RoutingState{Mode: ModeNormal, Generation: 4, ObservedAt: time.Now().UTC()}
	store := &testStore{state: state, ok: true}
	reader := &testReader{err: errors.New("temporary read failure")}
	sink := &testSink{}
	c := newCoordinatorForTest(t, reader, sink, store)
	if err := c.PollOnce(context.Background()); err == nil {
		t.Fatal("expected read error")
	}
	if len(sink.states) != 0 || len(store.saves) != 0 {
		t.Fatal("read error changed routing state")
	}
	reader.err = nil
	reader.snaps = []QuotaSnapshot{{OrdinaryUsageAllowed: quotaAllowed(false)}}
	sink.err = errors.New("controller unavailable")
	if err := c.PollOnce(context.Background()); err == nil {
		t.Fatal("expected sink error")
	}
	if len(store.saves) != 1 || store.saves[0].Mode != ModeQuotaFallback || store.saves[0].Generation != 5 {
		t.Fatalf("pending state not persisted before publication: %+v", store.saves)
	}
	sink.err = nil
	reader.snaps = []QuotaSnapshot{{OrdinaryUsageAllowed: quotaAllowed(false)}}
	if err := c.PollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(sink.calls) != 2 || sink.calls[0] != sink.calls[1] || sink.calls[1].Generation != 5 {
		t.Fatalf("uncertain publish retry changed idempotency key: %+v", sink.calls)
	}
}

func TestCoordinatorSerializesPollsAndCancellation(t *testing.T) {
	reader := &testReader{snaps: []QuotaSnapshot{{OrdinaryUsageAllowed: quotaAllowed(true)}}, started: make(chan struct{}, 1), release: make(chan struct{})}
	c := newCoordinatorForTest(t, reader, &testSink{}, &testStore{})
	firstDone := make(chan error, 1)
	go func() { firstDone <- c.PollOnce(context.Background()) }()
	<-reader.started
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.PollOnce(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("second poll error=%v", err)
	}
	close(reader.release)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
}

func TestHTTPModeSinkStatusAndSecretHandling(t *testing.T) {
	const token = "super-secret-token"
	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(token))
	}))
	defer server.Close()
	sink := HTTPModeSink{URL: server.URL, Token: token}
	err := sink.Publish(context.Background(), RoutingState{Mode: ModeNormal, Generation: 1, ObservedAt: time.Now()})
	if err == nil {
		t.Fatal("expected HTTP status error")
	}
	if strings.Contains(err.Error(), token) {
		t.Fatalf("error leaked token: %v", err)
	}
	if gotAuth != "Bearer "+token {
		t.Fatalf("authorization=%q", gotAuth)
	}
}

func TestHTTPModeSinkCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		<-req.Context().Done()
		return nil, req.Context().Err()
	})
	sink := HTTPModeSink{URL: "http://127.0.0.1:1", Token: "token", Client: &http.Client{Transport: transport}}
	done := make(chan error, 1)
	go func() {
		done <- sink.Publish(ctx, RoutingState{Mode: ModeNormal, Generation: 1, ObservedAt: time.Now()})
	}()
	time.Sleep(10 * time.Millisecond)
	cancel()
	if err := <-done; err == nil {
		t.Fatal("expected cancellation error")
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }
