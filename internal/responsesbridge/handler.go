package responsesbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"
)

type BridgeConfig struct {
	UpstreamURL      string
	APIKey           string
	UserAgent        string
	Model            string
	Client           *http.Client
	UpstreamTimeout  time.Duration
	MaxRequestBytes  int64
	MaxActiveStreams int
}

type Bridge struct {
	config    BridgeConfig
	upstream  Upstream
	semaphore chan struct{}
}

func NewBridge(config BridgeConfig) *Bridge {
	if config.Model == "" {
		config.Model = DefaultModel
	}
	if config.MaxRequestBytes <= 0 {
		config.MaxRequestBytes = 2 << 20
	}
	if config.MaxActiveStreams <= 0 {
		config.MaxActiveStreams = 16
	}
	return &Bridge{config: config, upstream: HTTPUpstream{URL: config.UpstreamURL, APIKey: config.APIKey, UserAgent: config.UserAgent, Client: config.Client, Timeout: config.UpstreamTimeout}, semaphore: make(chan struct{}, config.MaxActiveStreams)}
}

func (b *Bridge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && r.URL.Path == "/healthz" {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"status":"ok"}`)
		return
	}
	if r.URL.Path != "/v1/responses" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	requestID := nextID("req")
	request, err := readRequest(r, b.config.MaxRequestBytes)
	if err != nil {
		writeLocalError(w, http.StatusBadRequest, "invalid_request", err, requestID)
		return
	}
	if request.Model == "" {
		request.Model = b.config.Model
	}
	if request.Model != b.config.Model {
		writeLocalError(w, http.StatusBadRequest, "unknown_model", fmt.Errorf("model %q is not configured", request.Model), requestID)
		return
	}
	chat, toolNames, err := BuildChatRequestWithToolMap(request)
	if err != nil {
		writeLocalError(w, http.StatusBadRequest, "invalid_request", err, requestID)
		return
	}
	select {
	case b.semaphore <- struct{}{}:
	case <-r.Context().Done():
		writeLocalError(w, http.StatusRequestTimeout, "request_cancelled", r.Context().Err(), requestID)
		return
	}
	defer func() { <-b.semaphore }()
	upstreamStarted := time.Now()
	resp, err := b.upstream.Do(r.Context(), chat)
	if err != nil {
		log.Printf("bridge request_id=%s provider=cc_hub model=%s upstream_status=%d stream_started=false stream_completed=false tool_call_count=0 error_class=%s latency_ms=%d", requestID, request.Model, localStatus(err), errorClass(err), time.Since(upstreamStarted).Milliseconds())
		writeLocalError(w, localStatus(err), errorClass(err), err, requestID)
		return
	}
	defer resp.Body.Close()
	if request.Stream {
		b.streamResponse(w, r.Context(), resp, requestID, request.Model, upstreamStarted, toolNames)
		return
	}
	b.nonStreamResponse(w, resp, requestID, toolNames)
}

func readRequest(r *http.Request, limit int64) (ResponsesRequest, error) {
	var request ResponsesRequest
	body, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil {
		return request, err
	}
	if int64(len(body)) > limit {
		return request, fmt.Errorf("request exceeds %d bytes", limit)
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return request, fmt.Errorf("decode Responses request: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return request, fmt.Errorf("request must contain one JSON value")
		}
		return request, err
	}
	if request.Input == nil {
		return request, fmt.Errorf("input is required")
	}
	if err := validateInputEnvelope(request.Input); err != nil {
		return request, err
	}
	return request, nil
}

func (b *Bridge) streamResponse(w http.ResponseWriter, ctx context.Context, upstream *http.Response, requestID, model string, started time.Time, toolNames *ToolNameMap) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	responseID := nextID("resp")
	created := responseObject(responseID, "in_progress", []any{}, nil)
	if err := writeEvent(w, "response.created", map[string]any{"type": "response.created", "response": created}); err != nil {
		return
	}
	if err := writeEvent(w, "response.in_progress", map[string]any{"type": "response.in_progress", "response": created}); err != nil {
		return
	}
	flush(w)
	messageID := nextID("msg")
	messageAdded := false
	fullText := ""
	toolCalls := map[int]*AssembledToolCall{}
	var usage *ChatUsage
	processDelta := func(delta ChatDelta) (bool, error) {
		if delta.Usage != nil {
			usage = delta.Usage
		}
		if delta.Text != "" {
			if !messageAdded {
				messageAdded = true
				if err := writeEvent(w, "response.output_item.added", map[string]any{"type": "response.output_item.added", "output_index": 0, "item": map[string]any{"type": "message", "id": messageID, "status": "in_progress", "role": "assistant", "content": []any{}}}); err != nil {
					return false, err
				}
				if err := writeEvent(w, "response.content_part.added", map[string]any{"type": "response.content_part.added", "item_id": messageID, "output_index": 0, "content_index": 0, "part": map[string]any{"type": "output_text", "text": "", "annotations": []any{}}}); err != nil {
					return false, err
				}
			}
			fullText += delta.Text
			if err := writeEvent(w, "response.output_text.delta", map[string]any{"type": "response.output_text.delta", "delta": delta.Text, "item_id": messageID, "output_index": 0, "content_index": 0}); err != nil {
				return false, err
			}
			flush(w)
		}
		for _, call := range delta.ToolCalls {
			item := toolCalls[call.Index]
			if item == nil {
				item = &AssembledToolCall{}
				toolCalls[call.Index] = item
			}
			if call.ID != "" {
				item.ID = call.ID
			}
			item.Name += call.Name
			item.Arguments += call.Arguments
		}
		if !delta.Done {
			return false, nil
		}
		output := make([]any, 0, len(toolCalls)+1)
		if messageAdded {
			message := messageOutputItem(messageID, fullText)
			output = append(output, message)
			if err := writeEvent(w, "response.output_text.done", map[string]any{"type": "response.output_text.done", "item_id": messageID, "output_index": 0, "content_index": 0, "text": fullText}); err != nil {
				return false, err
			}
			if err := writeEvent(w, "response.content_part.done", map[string]any{"type": "response.content_part.done", "item_id": messageID, "output_index": 0, "content_index": 0, "part": map[string]any{"type": "output_text", "text": fullText, "annotations": []any{}}}); err != nil {
				return false, err
			}
			if err := writeEvent(w, "response.output_item.done", map[string]any{"type": "response.output_item.done", "output_index": 0, "item": message}); err != nil {
				return false, err
			}
		}
		indexes := sortedCallIndexes(toolCalls)
		for i, index := range indexes {
			call, err := toolNames.restoreCall(*toolCalls[index])
			if err != nil {
				return false, err
			}
			outputIndex := i
			if messageAdded {
				outputIndex++
			}
			itemID := nextID("fc")
			item := functionOutputItem(itemID, call)
			if err := writeEvent(w, "response.output_item.added", map[string]any{"type": "response.output_item.added", "output_index": outputIndex, "item": map[string]any{"type": "function_call", "id": itemID, "status": "in_progress", "call_id": call.ID, "name": call.Name, "arguments": ""}}); err != nil {
				return false, err
			}
			if err := writeEvent(w, "response.function_call_arguments.done", map[string]any{"type": "response.function_call_arguments.done", "output_index": outputIndex, "item_id": itemID, "call_id": call.ID, "name": call.Name, "arguments": call.Arguments}); err != nil {
				return false, err
			}
			if err := writeEvent(w, "response.output_item.done", map[string]any{"type": "response.output_item.done", "output_index": outputIndex, "item": item}); err != nil {
				return false, err
			}
			output = append(output, item)
		}
		completed := responseObject(responseID, "completed", output, usage)
		if err := writeEvent(w, "response.completed", map[string]any{"type": "response.completed", "response": completed}); err != nil {
			return false, err
		}
		flush(w)
		return true, nil
	}
	type streamUpdate struct {
		delta ChatDelta
		err   error
	}
	updates := make(chan streamUpdate, 16)
	go func() {
		err := StreamChatSSE(upstream.Body, func(delta ChatDelta) error {
			select {
			case updates <- streamUpdate{delta: delta}:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
		select {
		case updates <- streamUpdate{err: err}:
		case <-ctx.Done():
		}
	}()
	heartbeat := time.NewTicker(500 * time.Millisecond)
	defer heartbeat.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Printf("bridge request_id=%s response_id=%s provider=cc_hub model=%s upstream_status=%d stream_started=true stream_completed=false text_bytes=%d tool_call_count=%d error_class=%s latency_ms=%d", requestID, responseID, model, upstream.StatusCode, len(fullText), len(toolCalls), streamErrorClass(ctx.Err()), time.Since(started).Milliseconds())
			return
		case <-heartbeat.C:
			if err := writeEvent(w, "response.in_progress", map[string]any{"type": "response.in_progress", "response": created}); err != nil {
				return
			}
			flush(w)
		case update := <-updates:
			if update.err != nil {
				log.Printf("bridge request_id=%s response_id=%s provider=cc_hub model=%s upstream_status=%d stream_started=true stream_completed=false text_bytes=%d tool_call_count=%d error_class=%s error_detail=%q latency_ms=%d", requestID, responseID, model, upstream.StatusCode, len(fullText), len(toolCalls), streamErrorClass(update.err), streamErrorDiagnostic(update.err), time.Since(started).Milliseconds())
				_ = writeEvent(w, "error", map[string]any{"type": "error", "error": map[string]any{"type": streamErrorClass(update.err), "message": "Responses bridge upstream stream failed", "request_id": requestID, "response_id": responseID}})
				flush(w)
				return
			}
			completed, err := processDelta(update.delta)
			if err != nil {
				log.Printf("bridge request_id=%s response_id=%s provider=cc_hub model=%s upstream_status=%d stream_started=true stream_completed=false text_bytes=%d tool_call_count=%d error_class=%s error_detail=%q latency_ms=%d", requestID, responseID, model, upstream.StatusCode, len(fullText), len(toolCalls), streamErrorClass(err), streamErrorDiagnostic(err), time.Since(started).Milliseconds())
				_ = writeEvent(w, "error", map[string]any{"type": "error", "error": map[string]any{"type": streamErrorClass(err), "message": "Responses bridge stream translation failed", "request_id": requestID, "response_id": responseID}})
				flush(w)
				return
			}
			if completed {
				log.Printf("bridge request_id=%s response_id=%s provider=cc_hub model=%s upstream_status=%d stream_started=true stream_completed=true text_bytes=%d tool_call_count=%d error_class=none latency_ms=%d", requestID, responseID, model, upstream.StatusCode, len(fullText), len(toolCalls), time.Since(started).Milliseconds())
				return
			}
		}
	}
}

func sortedCallIndexes(calls map[int]*AssembledToolCall) []int {
	indexes := make([]int, 0, len(calls))
	for index := range calls {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	return indexes
}

func flush(w http.ResponseWriter) {
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

func (b *Bridge) nonStreamResponse(w http.ResponseWriter, resp *http.Response, requestID string, toolNames *ToolNameMap) {
	body, err := io.ReadAll(io.LimitReader(resp.Body, b.config.MaxRequestBytes+1))
	if err != nil {
		writeLocalError(w, http.StatusBadGateway, "upstream_read", err, requestID)
		return
	}
	if int64(len(body)) > b.config.MaxRequestBytes {
		writeLocalError(w, http.StatusBadGateway, "malformed_upstream", fmt.Errorf("upstream response exceeds limit"), requestID)
		return
	}
	var payload struct {
		Choices []struct {
			Message ChatMessage `json:"message"`
		} `json:"choices"`
		Usage *ChatUsage `json:"usage"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		writeLocalError(w, http.StatusBadGateway, "malformed_upstream", err, requestID)
		return
	}
	if len(payload.Choices) != 1 {
		writeLocalError(w, http.StatusBadGateway, "malformed_upstream", fmt.Errorf("upstream response must contain exactly one choice"), requestID)
		return
	}
	message := payload.Choices[0].Message
	if message.Content == "" && len(message.ToolCalls) == 0 {
		writeLocalError(w, http.StatusBadGateway, "malformed_upstream", fmt.Errorf("upstream response contains no assistant output"), requestID)
		return
	}
	responseID := nextID("resp")
	output := []any{}
	if message.Content != "" {
		output = append(output, messageOutputItem(nextID("msg"), message.Content))
	}
	seenCallIDs := map[string]struct{}{}
	for _, call := range message.ToolCalls {
		if call.Type != "function" || call.ID == "" || call.Function.Name == "" || call.Function.Arguments == "" {
			writeLocalError(w, http.StatusBadGateway, "malformed_upstream", fmt.Errorf("upstream tool call is incomplete"), requestID)
			return
		}
		if _, exists := seenCallIDs[call.ID]; exists {
			writeLocalError(w, http.StatusBadGateway, "malformed_upstream", fmt.Errorf("upstream tool call has duplicate call_id %q", call.ID), requestID)
			return
		}
		if !json.Valid([]byte(call.Function.Arguments)) {
			writeLocalError(w, http.StatusBadGateway, "malformed_upstream", fmt.Errorf("upstream tool call has invalid JSON arguments"), requestID)
			return
		}
		seenCallIDs[call.ID] = struct{}{}
		restored, err := toolNames.restoreCall(AssembledToolCall{ID: call.ID, Name: call.Function.Name, Arguments: call.Function.Arguments})
		if err != nil {
			writeLocalError(w, http.StatusBadGateway, "malformed_upstream", err, requestID)
			return
		}
		output = append(output, functionOutputItem(nextID("fc"), restored))
	}
	writeJSON(w, http.StatusOK, responseObject(responseID, "completed", output, payload.Usage))
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeLocalError(w http.ResponseWriter, status int, class string, err error, requestID string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"type": class, "message": err.Error(), "request_id": requestID}})
}

func errorClass(err error) string {
	if e, ok := err.(*UpstreamError); ok {
		return "upstream_" + e.Class
	}
	if strings.Contains(err.Error(), "network") {
		return "upstream_network"
	}
	return "upstream_failure"
}
