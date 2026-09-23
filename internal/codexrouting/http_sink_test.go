package codexrouting

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHTTPModeSinkCoordinatorFailureResponsesPreservePendingState(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			initial := RoutingState{
				Mode:       ModeNormal,
				Generation: 7,
				ObservedAt: time.Date(2026, 9, 24, 1, 2, 3, 4, time.UTC),
			}
			var requests int
			var accepted bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				w.WriteHeader(status)
				accepted = status >= http.StatusOK && status < http.StatusMultipleChoices
			}))
			defer server.Close()

			store := &testStore{state: initial, ok: true}
			reader := &testReader{snaps: []QuotaSnapshot{{OrdinaryUsageAllowed: quotaAllowed(true)}}}
			coordinator := newCoordinatorForTest(t, reader, &HTTPModeSink{
				URL:   server.URL,
				Token: "test-token",
			}, store)

			if err := coordinator.PollOnce(context.Background()); err == nil {
				t.Fatalf("PollOnce succeeded for HTTP %d", status)
			}
			if requests != 1 {
				t.Fatalf("HTTP requests=%d, want 1", requests)
			}
			if accepted {
				t.Fatal("failure response was treated as an accepted publication")
			}
			if store.state != initial || !store.ok {
				t.Fatalf("persisted state changed after HTTP %d: got=%+v want=%+v", status, store.state, initial)
			}
			if len(store.saves) != 0 {
				t.Fatalf("persisted generation changed after HTTP %d: saves=%+v", status, store.saves)
			}
		})
	}
}

func TestHTTPModeSinkCoordinatorTimeoutPreservesPendingState(t *testing.T) {
	initial := RoutingState{
		Mode:       ModeNormal,
		Generation: 11,
		ObservedAt: time.Date(2026, 9, 24, 2, 3, 4, 5, time.UTC),
	}
	requestSeen := make(chan struct{}, 1)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestSeen <- struct{}{}
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer func() {
		close(release)
		server.Close()
	}()

	store := &testStore{state: initial, ok: true}
	reader := &testReader{snaps: []QuotaSnapshot{{OrdinaryUsageAllowed: quotaAllowed(true)}}}
	coordinator := newCoordinatorForTest(t, reader, &HTTPModeSink{
		URL:   server.URL,
		Token: "test-token",
	}, store)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := coordinator.PollOnce(ctx)
	if err == nil {
		t.Fatal("PollOnce succeeded after the controller request timed out")
	}
	select {
	case <-requestSeen:
	case <-time.After(time.Second):
		t.Fatal("httptest server did not receive the sink request")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout error=%v, want context deadline", err)
	}
	if store.state != initial || !store.ok {
		t.Fatalf("persisted state changed after timeout: got=%+v want=%+v", store.state, initial)
	}
	if len(store.saves) != 0 {
		t.Fatalf("persisted generation changed after timeout: saves=%+v", store.saves)
	}
}

func TestHTTPModeSinkCoordinatorAcceptsSuccessfulPublication(t *testing.T) {
	initial := RoutingState{
		Mode:       ModeQuotaFallback,
		Generation: 13,
		ObservedAt: time.Date(2026, 9, 24, 3, 4, 5, 6, time.UTC),
	}
	var payload struct {
		Mode       Mode   `json:"mode"`
		ObservedAt string `json:"observed_at"`
		Generation int64  `json:"generation"`
	}
	accepted := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method=%s, want POST", r.Method)
		}
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("authorization=%q", r.Header.Get("Authorization"))
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		accepted = true
	}))
	defer server.Close()

	store := &testStore{state: initial, ok: true}
	reader := &testReader{snaps: []QuotaSnapshot{{OrdinaryUsageAllowed: quotaAllowed(false)}}}
	coordinator := newCoordinatorForTest(t, reader, &HTTPModeSink{
		URL:   server.URL,
		Token: "test-token",
	}, store)

	if err := coordinator.PollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !accepted {
		t.Fatal("successful 2xx response was not accepted")
	}
	if payload.Mode != initial.Mode || payload.Generation != initial.Generation || payload.ObservedAt != initial.ObservedAt.Format(time.RFC3339Nano) {
		t.Fatalf("payload=%+v, want state=%+v", payload, initial)
	}
	if store.state != initial || len(store.saves) != 0 {
		t.Fatalf("successful republish changed durable state: state=%+v saves=%+v", store.state, store.saves)
	}
}
