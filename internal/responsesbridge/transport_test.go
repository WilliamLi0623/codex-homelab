package responsesbridge

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPUpstreamMapsProviderFailuresWithoutRetry(t *testing.T) {
	for _, want := range []struct {
		status int
		class  string
	}{
		{http.StatusBadRequest, "invalid_request"},
		{http.StatusUnauthorized, "authentication"},
		{http.StatusTooManyRequests, "rate_limit"},
		{http.StatusServiceUnavailable, "provider_failure"},
	} {
		calls := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			w.WriteHeader(want.status)
		}))
		_, err := (HTTPUpstream{URL: server.URL}).Do(context.Background(), ChatRequest{Model: DefaultModel})
		server.Close()
		failure, ok := err.(*UpstreamError)
		if !ok || failure.Status != want.status || failure.Class != want.class {
			t.Fatalf("status %d error = %#v", want.status, err)
		}
		if calls != 1 {
			t.Fatalf("status %d was retried %d times", want.status, calls-1)
		}
	}
}

func TestLocalErrorsRemainDistinct(t *testing.T) {
	for _, status := range []int{400, 401, 403, 429, 500, 502, 503, 504} {
		if got := localStatus(&UpstreamError{Status: status, Class: "test"}); got != status {
			t.Errorf("localStatus(%d) = %d", status, got)
		}
	}
	if got := localStatus(context.DeadlineExceeded); got != 504 {
		t.Errorf("timeout status = %d", got)
	}
	if got := localStatus(errors.New("connection reset")); got != 502 {
		t.Errorf("network status = %d", got)
	}
}

func TestHTTPUpstreamDoesNotLeakProviderErrorBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"super-secret-provider-detail"}`))
	}))
	defer server.Close()
	_, err := (HTTPUpstream{URL: server.URL}).Do(context.Background(), ChatRequest{Model: DefaultModel})
	if err == nil || strings.Contains(err.Error(), "super-secret-provider-detail") {
		t.Fatalf("provider body leaked: %v", err)
	}
}
