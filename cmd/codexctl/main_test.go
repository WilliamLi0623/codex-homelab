package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDoctorReportsHealthyController(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/health" {
			t.Fatalf("path = %q, want /v1/health", request.URL.Path)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"status":"ok"}`))
	}))
	t.Cleanup(server.Close)

	var output bytes.Buffer
	if err := run([]string{"doctor", "--endpoint", server.URL}, &output, server.Client()); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if output.String() != "controller: ok\n" {
		t.Fatalf("output = %q, want controller health", output.String())
	}
}
