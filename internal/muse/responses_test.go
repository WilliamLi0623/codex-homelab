package muse

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestResponsesRequestEncodesMuseTerminalTool(t *testing.T) {
	req := Request{
		Model:     "muse-spark-1.3-contributor",
		Input:     []InputItem{{Role: "user", Text: "create the marker"}},
		Tools:     []Tool{{Type: "function", Name: "terminal", Description: "run in workspace", Parameters: map[string]any{"type": "object"}}},
		Store:     false,
		Reasoning: &Reasoning{Effort: "xhigh"},
	}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got["model"] != req.Model || got["store"] != false {
		t.Fatalf("request metadata = %#v", got)
	}
	reasoning, ok := got["reasoning"].(map[string]any)
	if !ok || reasoning["effort"] != "xhigh" {
		t.Fatalf("reasoning = %#v", got["reasoning"])
	}
	tools, ok := got["tools"].([]any)
	if !ok || len(tools) != 1 || tools[0].(map[string]any)["name"] != "terminal" {
		t.Fatalf("tools = %#v", got["tools"])
	}
}

func TestResponsesRequestEncodesFunctionCallHistory(t *testing.T) {
	req := Request{Input: []InputItem{{Type: "function_call", CallID: "call-1", Name: "terminal", Arguments: `{"command":"printf ok"}`}}}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Input []struct {
			Type      string `json:"type"`
			CallID    string `json:"call_id"`
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"input"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Input) != 1 || got.Input[0].Type != "function_call" || got.Input[0].CallID != "call-1" || got.Input[0].Name != "terminal" || !strings.Contains(got.Input[0].Arguments, "printf ok") {
		t.Fatalf("input = %#v", got.Input)
	}
}

func TestDecodeResponseToolCallAndFinalMessage(t *testing.T) {
	toolResponse := `{"id":"resp-1","status":"requires_action","output":[{"type":"function_call","call_id":"call-1","name":"terminal","arguments":"{\"command\":\"printf ok > marker.txt\"}"}]}`
	resp, err := DecodeResponse([]byte(toolResponse))
	if err != nil {
		t.Fatal(err)
	}
	calls := resp.ToolCalls()
	if len(calls) != 1 || calls[0].CallID != "call-1" || calls[0].Name != "terminal" || !strings.Contains(calls[0].Arguments, "marker.txt") {
		t.Fatalf("calls = %#v", calls)
	}
	final, err := DecodeResponse([]byte(`{"id":"resp-2","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"done"}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := final.FinalText(); got != "done" {
		t.Fatalf("FinalText() = %q", got)
	}
}

func TestDecodeResponseRejectsMissingIDAndMalformedJSON(t *testing.T) {
	if _, err := DecodeResponse([]byte(`{"status":"completed"}`)); err == nil || !strings.Contains(err.Error(), "response ID") {
		t.Fatalf("missing ID error = %v", err)
	}
	if _, err := DecodeResponse([]byte(`{"id":`)); err == nil {
		t.Fatal("malformed response accepted")
	}
}
