package responsesbridge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

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

func TestBuildChatRequestRejectsUnsupportedResponsesControls(t *testing.T) {
	for name, input := range map[string]string{
		"tool choice":         `{"model":"glm-5.3-flash","input":"hello","tool_choice":"required"}`,
		"parallel tool calls": `{"model":"glm-5.3-flash","input":"hello","parallel_tool_calls":false}`,
	} {
		t.Run(name, func(t *testing.T) {
			var req ResponsesRequest
			if err := json.Unmarshal([]byte(input), &req); err != nil {
				t.Fatal(err)
			}
			if _, err := BuildChatRequest(req); err == nil {
				t.Fatal("expected unsupported Responses control error")
			}
		})
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
