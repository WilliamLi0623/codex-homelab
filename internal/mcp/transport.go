package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/WilliamLi0623/codex-homelab/internal/store"
)

const (
	defaultProtocolVersion = "2025-06-18"
	maxTransportPayload    = 1 << 20
)

var ErrUnauthorized = errors.New("MCP authentication failed")

type Authenticator interface {
	Authenticate(*http.Request) error
}

type AuthenticatorFunc func(*http.Request) error

func (f AuthenticatorFunc) Authenticate(request *http.Request) error {
	return f(request)
}

type Transport struct {
	Server *Server
	Auth   Authenticator
}

func NewTransport(server *Server, auth Authenticator) *Transport {
	return &Transport{Server: server, Auth: auth}
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type initializeParams struct {
	ProtocolVersion string         `json:"protocolVersion"`
	Capabilities    map[string]any `json:"capabilities"`
	ClientInfo      map[string]any `json:"clientInfo"`
	Meta            map[string]any `json:"_meta"`
}

type initializeResult struct {
	ProtocolVersion string `json:"protocolVersion"`
	Capabilities    struct {
		Tools map[string]any `json:"tools"`
	} `json:"capabilities"`
	ServerInfo struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"serverInfo"`
}

type toolsListResult struct {
	Tools []Tool `json:"tools"`
}

type toolCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type toolCallResult struct {
	Content []toolContent `json:"content"`
	IsError bool          `json:"isError,omitempty"`
}

type toolContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func (t *Transport) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		writeHTTPError(writer, http.StatusMethodNotAllowed, "MCP transport requires POST")
		return
	}
	if t.Auth != nil {
		if err := t.Auth.Authenticate(request); err != nil {
			writer.Header().Set("WWW-Authenticate", "Bearer")
			writeHTTPError(writer, http.StatusUnauthorized, "MCP authentication failed")
			return
		}
	}
	var input rpcRequest
	decoder := json.NewDecoder(http.MaxBytesReader(writer, request.Body, maxTransportPayload))
	if err := decoder.Decode(&input); err != nil {
		writeRPCError(writer, http.StatusBadRequest, nil, -32700, "Parse error")
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeRPCError(writer, http.StatusBadRequest, input.ID, -32700, "Parse error")
		return
	}
	if input.JSONRPC != "2.0" || strings.TrimSpace(input.Method) == "" {
		writeRPCError(writer, http.StatusOK, input.ID, -32600, "Invalid Request")
		return
	}

	result, rpcErr := t.handle(request, input)
	if rpcErr != nil {
		writeRPCError(writer, http.StatusOK, input.ID, rpcErr.Code, rpcErr.Message)
		return
	}
	if result == nil {
		// JSON-RPC notifications do not receive a response. The current
		// Controller surface has no mutating notification methods, but keeping
		// this branch makes the transport safe for notifications/initialized.
		writer.WriteHeader(http.StatusAccepted)
		return
	}
	writeRPCResult(writer, http.StatusOK, input.ID, result)
}

func (t *Transport) handle(request *http.Request, input rpcRequest) (any, *rpcError) {
	switch input.Method {
	case "initialize":
		var params initializeParams
		if err := decodeParams(input.Params, &params); err != nil {
			return nil, &rpcError{Code: -32602, Message: "Invalid params"}
		}
		result := initializeResult{ProtocolVersion: defaultProtocolVersion}
		result.Capabilities.Tools = map[string]any{}
		result.ServerInfo.Name = "codex-controller"
		result.ServerInfo.Version = "v3"
		return result, nil
	case "notifications/initialized":
		return nil, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		if err := decodeParams(input.Params, &struct{}{}); err != nil {
			return nil, &rpcError{Code: -32602, Message: "Invalid params"}
		}
		if t.Server == nil {
			return nil, &rpcError{Code: -32603, Message: "MCP server is not configured"}
		}
		return toolsListResult{Tools: t.Server.Tools()}, nil
	case "tools/call":
		var params toolCallParams
		if err := decodeParams(input.Params, &params); err != nil || strings.TrimSpace(params.Name) == "" {
			return nil, &rpcError{Code: -32602, Message: "Invalid params"}
		}
		if t.Server == nil {
			return nil, &rpcError{Code: -32603, Message: "MCP server is not configured"}
		}
		arguments := params.Arguments
		if len(arguments) == 0 || string(arguments) == "null" {
			arguments = json.RawMessage(`{}`)
		}
		value, err := t.Server.CallTool(request.Context(), params.Name, arguments)
		if err != nil {
			return toolCallResult{IsError: true, Content: []toolContent{{Type: "text", Text: toolErrorText(err)}}}, nil
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			return toolCallResult{IsError: true, Content: []toolContent{{Type: "text", Text: "tool result encoding failed"}}}, nil
		}
		return toolCallResult{Content: []toolContent{{Type: "text", Text: string(encoded)}}}, nil
	default:
		return nil, &rpcError{Code: -32601, Message: "Method not found"}
	}
}

func decodeParams(raw json.RawMessage, destination any) error {
	if len(raw) == 0 || string(raw) == "null" {
		raw = json.RawMessage(`{}`)
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("multiple JSON values")
	}
	return nil
}

func toolErrorText(err error) string {
	switch {
	case errors.Is(err, ErrInvalidArguments):
		return "invalid tool arguments"
	case errors.Is(err, ErrUnknownTool):
		return "unknown tool"
	case errors.Is(err, ErrDispatcherUnavailable):
		return "dispatcher unavailable"
	case errors.Is(err, store.ErrTaskNotFound):
		return "task not found"
	case errors.Is(err, store.ErrAttemptNotFound):
		return "attempt not found"
	default:
		return "tool execution failed"
	}
}

func writeRPCResult(writer http.ResponseWriter, status int, id json.RawMessage, result any) {
	encoded, err := json.Marshal(result)
	if err != nil {
		writeRPCError(writer, http.StatusInternalServerError, id, -32603, "Internal error")
		return
	}
	writeRPC(writer, status, rpcResponse{JSONRPC: "2.0", ID: responseID(id), Result: encoded})
}

func writeRPCError(writer http.ResponseWriter, status int, id json.RawMessage, code int, message string) {
	writeRPC(writer, status, rpcResponse{JSONRPC: "2.0", ID: responseID(id), Error: &rpcError{Code: code, Message: message}})
}

func writeRPC(writer http.ResponseWriter, status int, response rpcResponse) {
	encoded, err := json.Marshal(response)
	if err != nil || len(encoded) > maxTransportPayload {
		writer.WriteHeader(http.StatusInternalServerError)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_, _ = writer.Write(append(encoded, '\n'))
}

func writeHTTPError(writer http.ResponseWriter, status int, message string) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(map[string]string{"error": message})
}

func responseID(id json.RawMessage) json.RawMessage {
	if len(id) == 0 {
		return json.RawMessage("null")
	}
	return id
}
