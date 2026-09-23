package responsesbridge

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadRequestRejectsUnsupportedResponsesFields(t *testing.T) {
	for name, field := range map[string]string{
		"reasoning extra field": `"reasoning":{"effort":"high","unknown":true}`,
		"previous response id":  `"previous_response_id":"resp_123"`,
		"metadata":              `"metadata":{"trace":"x"}`,
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"glm-5.3-flash","input":"hello",`+field+`}`))
			if _, err := readRequest(req, 1<<20); err == nil {
				t.Fatalf("accepted unsupported field %s", field)
			}
		})
	}
}

func TestReadRequestAcceptsExplicitlyModeledFields(t *testing.T) {
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"glm-5.3-flash","instructions":"be concise","reasoning":{"effort":"max","summary":"auto"},"store":false,"include":["reasoning.encrypted_content"],"prompt_cache_key":"session_opaque_key","client_metadata":{"origin":"codex"},"input":"hello","tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}],"stream":true,"max_output_tokens":32}`))
	got, err := readRequest(req, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if got.Instructions != "be concise" || got.Reasoning == nil || got.Reasoning.Effort != "max" || got.Reasoning.Summary != "auto" || got.Store == nil || *got.Store || len(got.Include) != 1 || got.Include[0] != "reasoning.encrypted_content" || got.PromptCacheKey != "session_opaque_key" || string(got.ClientMetadata) != `{"origin":"codex"}` || !got.Stream || got.MaxOutputTokens != 32 || len(got.Tools) != 1 {
		t.Fatalf("modeled fields were not preserved: %#v", got)
	}
}

func TestBuildChatRequestMapsReasoningEffortWithoutReasoningText(t *testing.T) {
	var req ResponsesRequest
	if err := json.Unmarshal([]byte(`{"model":"glm-5.3-flash","reasoning":{"effort":"max","summary":"detailed"},"input":"hello"}`), &req); err != nil {
		t.Fatal(err)
	}
	chat, err := BuildChatRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(chat)
	if err != nil {
		t.Fatal(err)
	}
	if chat.ReasoningEffort != "max" || strings.Contains(string(encoded), "summary") || strings.Contains(string(encoded), "reasoning_content") {
		t.Fatalf("reasoning metadata was not safely translated: %#v JSON=%s", chat, encoded)
	}
}

func TestBuildChatRequestPreservesFunctionToolStrict(t *testing.T) {
	for name, strictJSON := range map[string]string{
		"absent": "",
		"true":   `,"strict":true`,
		"false":  `,"strict":false`,
	} {
		t.Run(name, func(t *testing.T) {
			body := `{"model":"glm-5.3-flash","input":"hello","tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}` + strictJSON + `}]}`
			req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body))
			got, err := readRequest(req, 1<<20)
			if err != nil {
				t.Fatal(err)
			}
			chat, err := BuildChatRequest(got)
			if err != nil {
				t.Fatal(err)
			}
			if len(chat.Tools) != 1 {
				t.Fatalf("tools = %d, want 1", len(chat.Tools))
			}
			if name == "absent" {
				if chat.Tools[0].Function.Strict != nil {
					t.Fatalf("strict = %v, want absent", *chat.Tools[0].Function.Strict)
				}
				return
			}
			want := name == "true"
			if chat.Tools[0].Function.Strict == nil || *chat.Tools[0].Function.Strict != want {
				t.Fatalf("strict = %v, want %t", chat.Tools[0].Function.Strict, want)
			}
		})
	}
}

func TestReadRequestRejectsUnknownNestedResponsesEnvelopeFields(t *testing.T) {
	for name, body := range map[string]string{
		"input item":    `{"model":"glm-5.3-flash","input":[{"type":"message","content":"hello","unexpected":"x"}]}`,
		"content part":  `{"model":"glm-5.3-flash","input":[{"type":"message","content":[{"type":"input_text","text":"hello","unexpected":"x"}]}]}`,
		"tool":          `{"model":"glm-5.3-flash","input":"hello","tools":[{"type":"function","name":"lookup","unexpected":"x"}]}`,
		"function tool": `{"model":"glm-5.3-flash","input":"hello","tools":[{"type":"function","function":{"name":"lookup","unexpected":"x"}}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body))
			if _, err := readRequest(req, 1<<20); err == nil {
				t.Fatal("accepted unknown nested Responses envelope field")
			}
		})
	}
}

func TestResponseToolUnknownFieldErrorReportsOnlyShape(t *testing.T) {
	var tool ResponseTool
	err := json.Unmarshal([]byte(`{"type":"namespace","name":"private-name","description":"private description","tools":[{"type":"function","name":"lookup","parameters":{"private":"schema"},"description":"private tool description"}],"private":"payload"}`), &tool)
	// Unknown fields must fail closed while the diagnostic summary stays
	// structural and safe enough to characterize the real Codex payload.
	if err == nil || !strings.Contains(err.Error(), `type="namespace"`) || !strings.Contains(err.Error(), "nested_tools") || !strings.Contains(err.Error(), "lookup") {
		t.Fatalf("error lacks protocol-shape diagnostics: %v", err)
	}
	for _, private := range []string{"private-name", "private description", "private tool description", "schema", "payload"} {
		if strings.Contains(err.Error(), private) {
			t.Fatalf("error leaked payload value %q: %v", private, err)
		}
	}
}

func TestReadRequestPreservesModeledNestedFieldsAndOpaquePayloads(t *testing.T) {
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"glm-5.3-flash","input":[{"id":"msg_opaque_1","type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]},{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{}"},{"type":"function_call_output","call_id":"call_1","output":{"arbitrary":true}}],"tools":[{"type":"function","name":"lookup","description":"desc","parameters":{"type":"object","properties":{"x":{"type":"string","x-vendor":"ok"}},"additionalProperties":false}}]}`))
	got, err := readRequest(req, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Tools) != 1 || got.Tools[0].Name != "lookup" || got.Tools[0].Description != "desc" || !strings.Contains(string(got.Tools[0].Parameters), `"x-vendor":"ok"`) {
		t.Fatalf("modeled tool fields or schema payload lost: %#v", got.Tools)
	}
	items, err := parseInput(got.Input)
	if err != nil {
		t.Fatal(err)
	}
	if items[0].ID != "msg_opaque_1" {
		t.Fatalf("input item ID lost: %#v", items[0])
	}
	chat, err := BuildChatRequest(got)
	if err != nil {
		t.Fatal(err)
	}
	if len(chat.Messages) != 3 || chat.Messages[2].Content != `{"arbitrary":true}` {
		t.Fatalf("opaque tool output payload lost: %#v", chat.Messages)
	}
}

func TestBuildChatRequestPreservesResponsesHistoryAndTools(t *testing.T) {
	input := loadFixture(t, "request_tool_continuation.json")
	var req ResponsesRequest
	if err := json.Unmarshal(input, &req); err != nil {
		t.Fatal(err)
	}

	chat, err := BuildChatRequest(req)
	if err != nil {
		t.Fatal(err)
	}

	if chat.Model != "glm-5.3-flash" || chat.Stream != true {
		t.Fatalf("unexpected model/stream: %#v", chat)
	}
	if len(chat.Messages) != 4 {
		t.Fatalf("messages = %d, want 4", len(chat.Messages))
	}
	if chat.Messages[0].Role != "system" || chat.Messages[1].Role != "user" {
		t.Fatalf("message ordering lost: %#v", chat.Messages)
	}
	if len(chat.Messages[2].ToolCalls) != 1 || chat.Messages[2].ToolCalls[0].ID != "call_pwd_01" {
		t.Fatalf("assistant tool call lost: %#v", chat.Messages[2])
	}
	if chat.Messages[3].Role != "tool" || chat.Messages[3].ToolCallID != "call_pwd_01" || chat.Messages[3].Content != "/workspace/demo" {
		t.Fatalf("tool result lost: %#v", chat.Messages[3])
	}
	if len(chat.Tools) != 1 || chat.Tools[0].Function.Name != "get_test_value" {
		t.Fatalf("tool definition lost: %#v", chat.Tools)
	}
}

func TestBuildChatRequestRejectsUnknownToolShape(t *testing.T) {
	var req ResponsesRequest
	if err := json.Unmarshal([]byte(`{"model":"glm-5.3-flash","input":[{"type":"function_call","call_id":"c","name":"x","arguments":"{}"}],"tools":[{"type":"custom"}]}`), &req); err != nil {
		t.Fatal(err)
	}
	if _, err := BuildChatRequest(req); err == nil {
		t.Fatal("expected unsupported tool error")
	}
}

func TestBuildChatRequestPreservesResponsesToolControls(t *testing.T) {
	for name, input := range map[string]string{
		"auto and parallel":   `{"model":"glm-5.3-flash","input":"hello","tool_choice":"auto","parallel_tool_calls":true}`,
		"required and serial": `{"model":"glm-5.3-flash","input":"hello","tool_choice":"required","parallel_tool_calls":false}`,
	} {
		t.Run(name, func(t *testing.T) {
			var req ResponsesRequest
			if err := json.Unmarshal([]byte(input), &req); err != nil {
				t.Fatal(err)
			}
			chat, err := BuildChatRequest(req)
			if err != nil {
				t.Fatal(err)
			}
			var choice string
			if err := json.Unmarshal(chat.ToolChoice, &choice); err != nil {
				t.Fatal(err)
			}
			if choice != "auto" && choice != "required" {
				t.Fatalf("tool_choice = %q", choice)
			}
			if chat.ParallelToolCalls == nil {
				t.Fatal("parallel_tool_calls was dropped")
			}
			wantParallel := name == "auto and parallel"
			if *chat.ParallelToolCalls != wantParallel {
				t.Fatalf("parallel_tool_calls = %t, want %t", *chat.ParallelToolCalls, wantParallel)
			}
		})
	}

	for _, unsupported := range []string{`"unknown"`, `{"type":"function","name":"lookup"}`} {
		var req ResponsesRequest
		if err := json.Unmarshal([]byte(`{"model":"glm-5.3-flash","input":"hello","tool_choice":`+unsupported+`}`), &req); err != nil {
			t.Fatal(err)
		}
		if _, err := BuildChatRequest(req); err == nil {
			t.Fatalf("accepted unsupported tool_choice %s", unsupported)
		}
	}
}

func TestBuildChatRequestValidatesFunctionCallPairings(t *testing.T) {
	for name, input := range map[string]string{
		"empty call id":    `{"model":"glm-5.3-flash","input":[{"type":"function_call","call_id":"","name":"x","arguments":"{}"}]}`,
		"empty name":       `{"model":"glm-5.3-flash","input":[{"type":"function_call","call_id":"c","name":"","arguments":"{}"}]}`,
		"empty arguments":  `{"model":"glm-5.3-flash","input":[{"type":"function_call","call_id":"c","name":"x","arguments":""}]}`,
		"unknown output":   `{"model":"glm-5.3-flash","input":[{"type":"function_call_output","call_id":"missing","output":"x"}]}`,
		"duplicate output": `{"model":"glm-5.3-flash","input":[{"type":"function_call","call_id":"c","name":"x","arguments":"{}"},{"type":"function_call_output","call_id":"c","output":"1"},{"type":"function_call_output","call_id":"c","output":"2"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			var req ResponsesRequest
			if err := json.Unmarshal([]byte(input), &req); err != nil {
				t.Fatal(err)
			}
			if _, err := BuildChatRequest(req); err == nil {
				t.Fatal("expected pairing validation error")
			}
		})
	}
	var missing ResponsesRequest
	if err := json.Unmarshal([]byte(`{"model":"glm-5.3-flash","input":[{"type":"function_call","call_id":"c","name":"x","arguments":"{}"}]}`), &missing); err != nil {
		t.Fatal(err)
	}
	if _, err := BuildChatRequest(missing); err == nil {
		t.Fatal("expected missing function_call_output error")
	}
}

func loadFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}
