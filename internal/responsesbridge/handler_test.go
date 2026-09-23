package responsesbridge

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func parseBridgeEvents(t *testing.T, body string) []map[string]any {
	t.Helper()
	var events []map[string]any
	for _, block := range strings.Split(body, "\n\n") {
		for _, line := range strings.Split(block, "\n") {
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			var event map[string]any
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
				t.Fatal(err)
			}
			events = append(events, event)
		}
	}
	return events
}

func TestBridgeTextStreamEmitsCompletedOnlyAfterUpstreamDone(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Fatalf("upstream path = %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"BRIDGE_OK\"}}]}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()

	bridge := NewBridge(BridgeConfig{UpstreamURL: upstream.URL, Model: "glm-5.3-flash"})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"glm-5.3-flash","stream":true,"input":"Reply with exactly BRIDGE_OK"}`))
	res := httptest.NewRecorder()
	bridge.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	body := res.Body.String()
	if !strings.Contains(body, "response.output_text.delta") || !strings.Contains(body, "response.completed") {
		t.Fatalf("missing Responses lifecycle: %s", body)
	}
	if strings.Count(body, `"type":"response.output_text.delta"`) != 1 {
		t.Fatalf("text delta count = %d", strings.Count(body, `"type":"response.output_text.delta"`))
	}
}

func TestBridgeSendsProgressHeartbeatDuringLongUpstreamThink(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer upstream.Close()

	bridgeServer := httptest.NewServer(NewBridge(BridgeConfig{UpstreamURL: upstream.URL, Model: DefaultModel}))
	defer bridgeServer.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, bridgeServer.URL+"/v1/responses", strings.NewReader(`{"model":"glm-5.3-flash","stream":true,"input":"think"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := bridgeServer.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	frames := make(chan string, 1)
	go func() {
		reader := bufio.NewReader(resp.Body)
		var frame strings.Builder
		count := 0
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			frame.WriteString(line)
			if line == "\n" {
				count++
				if count == 3 {
					frames <- frame.String()
					return
				}
				frame.Reset()
			}
		}
	}()
	select {
	case frame := <-frames:
		if !strings.Contains(frame, "event: response.in_progress") {
			t.Fatalf("third event was not an in-progress heartbeat: %q", frame)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("bridge did not send a progress event while upstream was idle")
	}
}

func TestBridgeTextStreamEmitsCompleteResponsesMessageLifecycle(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()

	bridge := NewBridge(BridgeConfig{UpstreamURL: upstream.URL, Model: DefaultModel})
	res := httptest.NewRecorder()
	bridge.ServeHTTP(res, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"glm-5.3-flash","stream":true,"input":"hello"}`)))

	var lifecycle []string
	for _, event := range parseBridgeEvents(t, res.Body.String()) {
		lifecycle = append(lifecycle, event["type"].(string))
	}
	want := []string{
		"response.created",
		"response.in_progress",
		"response.output_item.added",
		"response.content_part.added",
		"response.output_text.delta",
		"response.output_text.done",
		"response.content_part.done",
		"response.output_item.done",
		"response.completed",
	}
	if strings.Join(lifecycle, ",") != strings.Join(want, ",") {
		t.Fatalf("lifecycle = %v, want %v", lifecycle, want)
	}
}

func TestBridgeResponsesEventsUseOfficialEnvelopesAndUsageFields(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2,\"total_tokens\":5},\"choices\":[]}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()
	bridge := NewBridge(BridgeConfig{UpstreamURL: upstream.URL, Model: DefaultModel})
	res := httptest.NewRecorder()
	bridge.ServeHTTP(res, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"glm-5.3-flash","stream":true,"input":"hello"}`)))
	events := parseBridgeEvents(t, res.Body.String())
	created, completed := events[0], events[len(events)-1]
	if created["type"] != "response.created" || created["response"] == nil {
		t.Fatalf("created envelope = %#v", created)
	}
	response, ok := completed["response"].(map[string]any)
	if !ok || completed["type"] != "response.completed" {
		t.Fatalf("completed envelope = %#v", completed)
	}
	if response["object"] != "response" {
		t.Fatalf("response object = %#v", response)
	}
	usage := response["usage"].(map[string]any)
	if usage["input_tokens"] != float64(3) || usage["output_tokens"] != float64(2) || usage["total_tokens"] != float64(5) {
		t.Fatalf("usage = %#v", usage)
	}
	if _, ok := response["output"].([]any); !ok {
		t.Fatalf("complete output missing: %#v", response)
	}
}

func TestBridgeMixedTextAndToolsUseDistinctOutputIndexes(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"text\",\"tool_calls\":[{\"index\":0,\"id\":\"call_a\",\"function\":{\"name\":\"a\",\"arguments\":\"{}\"}}]}}]}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()
	bridge := NewBridge(BridgeConfig{UpstreamURL: upstream.URL, Model: DefaultModel})
	res := httptest.NewRecorder()
	bridge.ServeHTTP(res, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"glm-5.3-flash","stream":true,"input":"hello","tools":[{"type":"function","name":"a","parameters":{"type":"object"}}]}`)))
	var indexes []float64
	var textIndex, argsIndex any
	for _, event := range parseBridgeEvents(t, res.Body.String()) {
		if event["type"] == "response.output_item.added" || event["type"] == "response.output_item.done" {
			if value, ok := event["output_index"].(float64); ok {
				indexes = append(indexes, value)
			}
		}
		if event["type"] == "response.output_text.delta" {
			textIndex = event["output_index"]
		}
		if event["type"] == "response.function_call_arguments.done" {
			argsIndex = event["output_index"]
		}
	}
	if len(indexes) != 4 || indexes[0] != 0 || indexes[1] != 0 || indexes[2] != 1 || indexes[3] != 1 {
		t.Fatalf("output indexes = %#v", indexes)
	}
	if textIndex != float64(0) || argsIndex != float64(1) {
		t.Fatalf("text/tool indexes = %v/%v", textIndex, argsIndex)
	}
}

func TestBridgeFlattensNamespaceToolAndRestoresCodexNamespaceCall(t *testing.T) {
	namespaceTool := ResponseTool{Type: "namespace", Name: "agents", Description: "Agent tools", NamespaceTools: []ResponseTool{{Type: "function", Name: "wait_agent", Description: "Wait", InputSchema: json.RawMessage(`{"type":"object"}`)}}}
	converted, _, err := convertTools([]ResponseTool{namespaceTool})
	if err != nil {
		t.Fatal(err)
	}
	chatName := converted[0].Function.Name
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var chat ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&chat); err != nil {
			t.Errorf("decode Chat request: %v", err)
		}
		if len(chat.Tools) != 1 || chat.Tools[0].Function.Name != chatName {
			t.Errorf("upstream namespace was not flattened deterministically: %#v", chat.Tools)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_ns_1\",\"type\":\"function\",\"function\":{\"name\":%q,\"arguments\":\"{}\"}}]}}]}\n\n", chatName)
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()
	bridge := NewBridge(BridgeConfig{UpstreamURL: upstream.URL, Model: DefaultModel})
	body, _ := json.Marshal(map[string]any{"model": DefaultModel, "stream": true, "input": "run agent tool", "tools": []ResponseTool{namespaceTool}})
	res := httptest.NewRecorder()
	bridge.ServeHTTP(res, httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body)))
	if !strings.Contains(res.Body.String(), `"type":"response.completed"`) || !strings.Contains(res.Body.String(), `"name":"wait_agent"`) || !strings.Contains(res.Body.String(), `"namespace":"agents"`) {
		t.Fatalf("namespace call was not restored in Responses output: %s", res.Body.String())
	}
}

func TestBridgeUpstreamFailureDoesNotEmitCompleted(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"provider unavailable"}}`, http.StatusServiceUnavailable)
	}))
	defer upstream.Close()

	bridge := NewBridge(BridgeConfig{UpstreamURL: upstream.URL, Model: "glm-5.3-flash"})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"glm-5.3-flash","stream":true,"input":"hello"}`))
	res := httptest.NewRecorder()
	bridge.ServeHTTP(res, req)
	if res.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	if strings.Contains(res.Body.String(), "response.completed") {
		t.Fatal("failed request emitted response.completed")
	}
}

func TestBridgeInterruptedStreamDoesNotEmitCompleted(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n")
	}))
	defer upstream.Close()

	bridge := NewBridge(BridgeConfig{UpstreamURL: upstream.URL, Model: "glm-5.3-flash"})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"glm-5.3-flash","stream":true,"input":"hello"}`))
	res := httptest.NewRecorder()
	bridge.ServeHTTP(res, req)
	if strings.Contains(res.Body.String(), "response.completed") {
		t.Fatal("interrupted stream emitted response.completed")
	}
	if !strings.Contains(res.Body.String(), "partial") {
		t.Fatalf("partial delta was not forwarded: %s", res.Body.String())
	}
	if !strings.Contains(res.Body.String(), "event: error") || !strings.Contains(res.Body.String(), "request_id") {
		t.Fatalf("missing safe stream error: %s", res.Body.String())
	}
}

func TestBridgeMalformedStreamEmitsStructuredError(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {not-json}\n\n")
	}))
	defer upstream.Close()
	bridge := NewBridge(BridgeConfig{UpstreamURL: upstream.URL, Model: DefaultModel})
	res := httptest.NewRecorder()
	bridge.ServeHTTP(res, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"glm-5.3-flash","stream":true,"input":"hello"}`)))
	if strings.Contains(res.Body.String(), "response.completed") || !strings.Contains(res.Body.String(), "malformed_sse") {
		t.Fatalf("malformed stream body = %s", res.Body.String())
	}
}

func TestBridgeEmptyDoneFixtureEmitsErrorWithoutCompleted(t *testing.T) {
	fixture := string(loadFixture(t, "stream_empty_done.sse"))
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, fixture)
	}))
	defer upstream.Close()

	bridge := NewBridge(BridgeConfig{UpstreamURL: upstream.URL, Model: DefaultModel})
	res := httptest.NewRecorder()
	bridge.ServeHTTP(res, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"glm-5.3-flash","stream":true,"input":"hello"}`)))
	body := res.Body.String()
	if strings.Contains(body, "response.completed") {
		t.Fatal("empty upstream stream emitted response.completed")
	}
	if !strings.Contains(body, "event: error") || !strings.Contains(body, `"type":"malformed_upstream"`) {
		t.Fatalf("missing sanitized empty-upstream error: %s", body)
	}
}

func TestBridgeStreamToolValidationPreventsCompleted(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"c\",\"function\":{\"name\":\"x\",\"arguments\":\"not-json\"}}]}}]}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()
	bridge := NewBridge(BridgeConfig{UpstreamURL: upstream.URL, Model: DefaultModel})
	res := httptest.NewRecorder()
	bridge.ServeHTTP(res, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"glm-5.3-flash","stream":true,"input":"hello","tools":[{"type":"function","name":"x","parameters":{"type":"object"}}]}`)))
	if strings.Contains(res.Body.String(), "response.completed") || !strings.Contains(res.Body.String(), "invalid_tool_call") {
		t.Fatalf("invalid tool stream body = %s", res.Body.String())
	}
}

func TestBridgeRejectsEmptySuccessfulChatResponse(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, `{}`) }))
	defer upstream.Close()
	bridge := NewBridge(BridgeConfig{UpstreamURL: upstream.URL, Model: DefaultModel})
	res := httptest.NewRecorder()
	bridge.ServeHTTP(res, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"glm-5.3-flash","input":"hello"}`)))
	if res.Code != http.StatusBadGateway || !strings.Contains(res.Body.String(), "malformed_upstream") {
		t.Fatalf("status/body = %d %s", res.Code, res.Body.String())
	}
}

func TestBridgeRejectsMultipleNonStreamChoices(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"one"}},{"message":{"content":"two"}}]}`)
	}))
	defer upstream.Close()
	bridge := NewBridge(BridgeConfig{UpstreamURL: upstream.URL, Model: DefaultModel})
	res := httptest.NewRecorder()
	bridge.ServeHTTP(res, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"glm-5.3-flash","input":"hello"}`)))
	if res.Code != http.StatusBadGateway || !strings.Contains(res.Body.String(), "malformed_upstream") {
		t.Fatalf("status/body = %d %s", res.Code, res.Body.String())
	}
}

func TestBridgeRejectsMalformedNonStreamToolCalls(t *testing.T) {
	for name, message := range map[string]string{
		"missing call id":       `{"tool_calls":[{"id":"","type":"function","function":{"name":"x","arguments":"{}"}}]}`,
		"duplicate call id":     `{"tool_calls":[{"id":"same","type":"function","function":{"name":"x","arguments":"{}"}},{"id":"same","type":"function","function":{"name":"y","arguments":"{}"}}]}`,
		"unsupported tool type": `{"tool_calls":[{"id":"c","type":"custom","function":{"name":"x","arguments":"{}"}}]}`,
		"empty name":            `{"tool_calls":[{"id":"c","type":"function","function":{"name":"","arguments":"{}"}}]}`,
		"non-json arguments":    `{"tool_calls":[{"id":"c","type":"function","function":{"name":"x","arguments":"not-json"}}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.WriteString(w, `{"choices":[{"message":`+message+`}]}`)
			}))
			defer upstream.Close()

			bridge := NewBridge(BridgeConfig{UpstreamURL: upstream.URL, Model: DefaultModel})
			res := httptest.NewRecorder()
			bridge.ServeHTTP(res, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"glm-5.3-flash","input":"hello"}`)))
			if res.Code != http.StatusBadGateway || !strings.Contains(res.Body.String(), "malformed_upstream") {
				t.Fatalf("status/body = %d %s", res.Code, res.Body.String())
			}
		})
	}
}

func TestBridgeRoutesOnlyExpectedEndpoints(t *testing.T) {
	bridge := NewBridge(BridgeConfig{UpstreamURL: "http://127.0.0.1:1", Model: "glm-5.3-flash"})
	for _, tc := range []struct {
		method string
		path   string
		status int
	}{
		{http.MethodGet, "/healthz", http.StatusOK},
		{http.MethodGet, "/v1/responses", http.StatusMethodNotAllowed},
		{http.MethodPost, "/v1/models", http.StatusNotFound},
	} {
		res := httptest.NewRecorder()
		req := httptest.NewRequest(tc.method, tc.path, nil).WithContext(context.Background())
		bridge.ServeHTTP(res, req)
		if res.Code != tc.status {
			t.Errorf("%s %s = %d, want %d", tc.method, tc.path, res.Code, tc.status)
		}
	}
}
