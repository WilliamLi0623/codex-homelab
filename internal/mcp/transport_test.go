package mcp

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/WilliamLi0623/codex-homelab/internal/store"
)

type tokenAuthenticator struct {
	token string
}

func (a tokenAuthenticator) Authenticate(request *http.Request) error {
	if request.Header.Get("Authorization") != "Bearer "+a.token {
		return ErrUnauthorized
	}
	return nil
}

func TestTransportInitialize(t *testing.T) {
	transport := newTestTransport(t, nil)
	response := callTransport(t, transport, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var body rpcResponse
	decodeJSON(t, response, &body)
	if body.Error != nil || body.Result == nil {
		t.Fatalf("initialize response = %+v, want result", body)
	}
	var result initializeResult
	if err := json.Unmarshal(body.Result, &result); err != nil {
		t.Fatalf("decode initialize result: %v", err)
	}
	if result.ServerInfo.Name == "" || result.Capabilities.Tools == nil {
		t.Fatalf("initialize result = %+v, want server info and tools capability", result)
	}
}

func TestTransportToolsListAndReadToolCall(t *testing.T) {
	transport := newTestTransport(t, nil)
	list := callTransport(t, transport, `{"jsonrpc":"2.0","id":"list","method":"tools/list","params":{}}`)
	if list.Code != http.StatusOK {
		t.Fatalf("tools/list status = %d, body = %s", list.Code, list.Body.String())
	}
	var listBody rpcResponse
	decodeJSON(t, list, &listBody)
	var toolsResult toolsListResult
	if err := json.Unmarshal(listBody.Result, &toolsResult); err != nil {
		t.Fatalf("decode tools/list result: %v", err)
	}
	if len(toolsResult.Tools) == 0 {
		t.Fatal("tools/list returned no tools")
	}

	call := callTransport(t, transport, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"list_tasks","arguments":{}}}`)
	if call.Code != http.StatusOK {
		t.Fatalf("tools/call status = %d, body = %s", call.Code, call.Body.String())
	}
	var callBody rpcResponse
	decodeJSON(t, call, &callBody)
	var result toolCallResult
	if err := json.Unmarshal(callBody.Result, &result); err != nil {
		t.Fatalf("decode tools/call result: %v", err)
	}
	if result.IsError || len(result.Content) != 1 || result.Content[0].Type != "text" {
		t.Fatalf("tools/call result = %+v, want one text result", result)
	}
}

func TestTransportWriteToolCallAndUnknownTool(t *testing.T) {
	transport := newTestTransport(t, nil)
	call := callTransport(t, transport, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"submit_task","arguments":{"repository":"owner/repository","base_ref":"main","objective":"test","idempotency_key":"transport-1"}}}`)
	if call.Code != http.StatusOK {
		t.Fatalf("submit_task status = %d, body = %s", call.Code, call.Body.String())
	}
	var callBody rpcResponse
	decodeJSON(t, call, &callBody)
	var result toolCallResult
	if err := json.Unmarshal(callBody.Result, &result); err != nil {
		t.Fatalf("decode submit_task result: %v", err)
	}
	if result.IsError {
		t.Fatalf("submit_task result = %+v, want success", result)
	}

	unknown := callTransport(t, transport, `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"missing","arguments":{}}}`)
	var unknownBody rpcResponse
	decodeJSON(t, unknown, &unknownBody)
	if unknownBody.Error != nil {
		t.Fatalf("unknown tool top-level error = %+v, want tool result error", unknownBody.Error)
	}
	if err := json.Unmarshal(unknownBody.Result, &result); err != nil {
		t.Fatalf("decode unknown tool result: %v", err)
	}
	if !result.IsError || len(result.Content) != 1 || !strings.Contains(result.Content[0].Text, "unknown tool") {
		t.Fatalf("unknown tool result = %+v, want isError", result)
	}
}

func TestTransportAcceptsStandardOptionalMCPFields(t *testing.T) {
	transport := newTestTransport(t, nil)

	list := callTransport(t, transport, `{"jsonrpc":"2.0","id":"list-meta","method":"tools/list","params":{"cursor":"page-2","_meta":{"progressToken":"p27"}}}`)
	if list.Code != http.StatusOK {
		t.Fatalf("tools/list with standard optional fields status = %d, body = %s", list.Code, list.Body.String())
	}
	var listBody rpcResponse
	decodeJSON(t, list, &listBody)
	if listBody.Error != nil {
		t.Fatalf("tools/list with standard optional fields error = %+v", listBody.Error)
	}

	call := callTransport(t, transport, `{"jsonrpc":"2.0","id":"call-meta","method":"tools/call","params":{"name":"list_tasks","arguments":{},"_meta":{"progressToken":"p27"}}}`)
	if call.Code != http.StatusOK {
		t.Fatalf("tools/call with standard optional fields status = %d, body = %s", call.Code, call.Body.String())
	}
	var callBody rpcResponse
	decodeJSON(t, call, &callBody)
	if callBody.Error != nil {
		t.Fatalf("tools/call with standard optional fields error = %+v", callBody.Error)
	}
}

func TestTransportRejectsMalformedJSONAndMissingAuth(t *testing.T) {
	transport := newTestTransport(t, tokenAuthenticator{token: "secret"})
	missingAuth := callTransport(t, transport, `{"jsonrpc":"2.0","id":1,"method":"initialize"}`)
	if missingAuth.Code != http.StatusUnauthorized {
		t.Fatalf("missing auth status = %d, want 401", missingAuth.Code)
	}

	request := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewBufferString("{"))
	request.Header.Set("Authorization", "Bearer secret")
	recorder := httptest.NewRecorder()
	transport.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("malformed JSON status = %d, want 400", recorder.Code)
	}
	var body rpcResponse
	decodeJSON(t, recorder, &body)
	if body.Error == nil || body.Error.Code != -32700 {
		t.Fatalf("malformed JSON error = %+v, want parse error", body.Error)
	}
}

func TestTransportReturnsNotFoundForOAuthDiscovery(t *testing.T) {
	transport := newTestTransport(t, tokenAuthenticator{token: "secret"})
	request := httptest.NewRequest(http.MethodGet, "/.well-known/oauth-protected-resource/mcp", nil)
	recorder := httptest.NewRecorder()
	transport.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("OAuth discovery status = %d, want 404", recorder.Code)
	}
}

func newTestTransport(t *testing.T, auth Authenticator) *Transport {
	t.Helper()
	database, err := store.Open(filepath.Join(t.TempDir(), "controller.sqlite"))
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return NewTransport(NewServer(database), auth)
}

func callTransport(t *testing.T, transport *Transport, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	transport.ServeHTTP(recorder, request)
	return recorder
}

func decodeJSON(t *testing.T, recorder *httptest.ResponseRecorder, destination any) {
	t.Helper()
	if err := json.NewDecoder(recorder.Body).Decode(destination); err != nil {
		t.Fatalf("decode JSON response: %v; body=%s", err, recorder.Body.String())
	}
}
