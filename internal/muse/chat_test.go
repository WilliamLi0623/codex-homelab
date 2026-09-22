package muse

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/net/http2"
)

func TestChatHTTPClientBridgesToolCallToResponses(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		var request chatRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.ToolChoice != "required" {
			t.Fatalf("tool_choice = %q, want required", request.ToolChoice)
		}
		if request.Model != "glm-5.3-flash" || len(request.Messages) != 1 || request.Messages[0].Role != "user" {
			t.Fatalf("request = %+v", request)
		}
		if len(request.Tools) != 1 || request.Tools[0].Function.Name != "terminal" {
			t.Fatalf("tools = %+v", request.Tools)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chat-1","choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","content":null,"tool_calls":[{"id":"call-1","type":"function","function":{"name":"terminal","arguments":"{\"command\":\"printf ok\"}"}}]}}]}`))
	}))
	defer server.Close()

	client := NewChatHTTPClient(server.URL, "test-key", server.Client())
	response, err := client.CreateResponse(context.Background(), Request{
		Model: "glm-5.3-flash",
		Input: []InputItem{{Role: "user", Text: "run it"}},
		Tools: []Tool{terminalTool()},
	})
	if err != nil {
		t.Fatal(err)
	}
	calls := response.ToolCalls()
	if response.ID != "chat-1" || len(calls) != 1 || calls[0].CallID != "call-1" || calls[0].Name != "terminal" || !strings.Contains(calls[0].Arguments, "printf ok") {
		t.Fatalf("response = %+v calls = %+v", response, calls)
	}
}

func TestChatHTTPClientEncodesExplicitReasoningEffort(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request chatRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.ReasoningEffort != "max" {
			t.Fatalf("reasoning_effort = %q, want max", request.ReasoningEffort)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chat-reasoning","choices":[{"finish_reason":"stop","message":{"content":"done"}}]}`))
	}))
	defer server.Close()

	_, err := NewChatHTTPClient(server.URL, "test-key", server.Client()).CreateResponse(context.Background(), Request{
		Model: "glm-5.3-flash", ReasoningEffort: "max", Input: []InputItem{{Role: "user", Text: "run"}},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestChatHTTPClientUsesExplicitHTTP2ByDefault(t *testing.T) {
	client := NewChatHTTPClient("https://cch.example/v1", "test-key", nil)
	if _, ok := client.HTTP.Transport.(*http2.Transport); !ok {
		t.Fatalf("transport = %T; want *http2.Transport", client.HTTP.Transport)
	}
}

func TestChatHTTPClientPreservesBoundedProviderError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":"no_available_providers"}`))
	}))
	defer server.Close()

	client := NewChatHTTPClient(server.URL, "test-key", server.Client())
	_, err := client.CreateResponse(context.Background(), Request{Model: "glm-5.3-flash", Input: []InputItem{{Role: "user", Text: "run"}}})
	if err == nil || !strings.Contains(err.Error(), "no_available_providers") {
		t.Fatalf("error = %v; want provider response body", err)
	}
}

func TestChatHTTPClientBridgesFunctionHistoryAndFinalText(t *testing.T) {
	var got chatRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chat-2","choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"done"}}]}`))
	}))
	defer server.Close()

	client := NewChatHTTPClient(server.URL, "test-key", server.Client())
	response, err := client.CreateResponse(context.Background(), Request{
		Model: "glm-5.3-flash",
		Input: []InputItem{
			{Role: "user", Text: "run it"},
			{Type: "function_call", CallID: "call-1", Name: "terminal", Arguments: `{"command":"printf ok"}`},
			{Type: "function_call_output", CallID: "call-1", Output: `{"stdout":"ok"}`},
		},
		Tools: []Tool{terminalTool()},
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.FinalText() != "done" {
		t.Fatalf("final text = %q", response.FinalText())
	}
	if len(got.Messages) != 3 || got.Messages[1].Role != "assistant" || len(got.Messages[1].ToolCalls) != 1 || got.Messages[2].Role != "tool" || got.Messages[2].ToolCallID != "call-1" {
		t.Fatalf("messages = %+v", got.Messages)
	}
}

func TestChatHTTPClientAllowsFinalResponseAfterToolResult(t *testing.T) {
	client := NewChatHTTPClient("https://cch.example/v1", "test-key", nil)
	if got := toolChoiceForRequest(Request{Tools: []Tool{terminalTool()}, Input: []InputItem{{Role: "user", Text: "run"}}}); got != "required" {
		t.Fatalf("initial tool_choice = %q, want required", got)
	}
	if got := toolChoiceForRequest(Request{Tools: []Tool{terminalTool()}, Input: []InputItem{{Type: "function_call_output", CallID: "call-1", Output: "ok"}}}); got != "" {
		t.Fatalf("continuation tool_choice = %q, want default auto", got)
	}
	_ = client
}
