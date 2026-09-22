package muse

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPClientCreatesResponsesRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/responses" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Fatalf("authorization header = %q", r.Header.Get("Authorization"))
		}
		var request Request
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.Model != "muse-spark-1.3-contributor" || len(request.Tools) != 1 {
			t.Fatalf("request = %+v", request)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp-1","status":"completed","output":[]}`))
	}))
	defer server.Close()

	client := NewHTTPClient(server.URL, "test-key", server.Client())
	response, err := client.CreateResponse(context.Background(), Request{Model: "muse-spark-1.3-contributor", Tools: []Tool{terminalTool()}})
	if err != nil {
		t.Fatal(err)
	}
	if response.ID != "resp-1" {
		t.Fatalf("response = %+v", response)
	}
}

func TestHTTPClientRejectsProviderErrorWithoutLeakingCredential(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("upstream failed"))
	}))
	defer server.Close()

	client := NewHTTPClient(server.URL, "secret-key", server.Client())
	_, err := client.CreateResponse(context.Background(), Request{Model: "muse"})
	if err == nil || !strings.Contains(err.Error(), "status 502") || strings.Contains(err.Error(), "secret-key") {
		t.Fatalf("error = %v", err)
	}
}

func TestHTTPClientNormalizesVersionedBaseURLAndAPIKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" || r.Header.Get("Authorization") != "Bearer test-key" {
			t.Fatalf("request = %s %s authorization=%q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte(`{"id":"resp-1","status":"completed","output":[]}`))
	}))
	defer server.Close()

	client := NewHTTPClient(server.URL+"/v1/", " test-key\n", server.Client())
	if _, err := client.CreateResponse(context.Background(), Request{Model: "muse"}); err != nil {
		t.Fatal(err)
	}
}
