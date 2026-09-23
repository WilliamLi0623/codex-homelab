package responsesbridge

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestConvertNamespaceToolsToDeterministicChatFunctions(t *testing.T) {
	strict := true
	tools := []ResponseTool{{Type: "namespace", Name: "agents", Description: "Agent tools", NamespaceTools: []ResponseTool{
		{Type: "function", Name: "wait_agent", Description: "Wait", InputSchema: json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}}}`), Strict: &strict},
	}}}
	chat, names, err := convertTools(tools)
	if err != nil {
		t.Fatal(err)
	}
	if len(chat) != 1 || chat[0].Type != "function" || !strings.HasPrefix(chat[0].Function.Name, "ns_") || len(chat[0].Function.Name) > 64 {
		t.Fatalf("flattened tool = %#v", chat)
	}
	if string(chat[0].Function.Parameters) != string(tools[0].NamespaceTools[0].InputSchema) || chat[0].Function.Strict == nil || !*chat[0].Function.Strict {
		t.Fatalf("nested schema/strict changed: %#v", chat[0].Function)
	}
	restored, err := names.restoreCall(AssembledToolCall{ID: "call_1", Name: chat[0].Function.Name, Arguments: `{}`})
	if err != nil || restored.Namespace != "agents" || restored.Name != "wait_agent" || restored.ID != "call_1" {
		t.Fatalf("restored call=%+v err=%v", restored, err)
	}
	chatAgain, _, err := convertTools(tools)
	if err != nil || chatAgain[0].Function.Name != chat[0].Function.Name {
		t.Fatalf("mapping is not deterministic: %#v err=%v", chatAgain, err)
	}
}

func TestConvertNamespaceToolsAvoidsNameCollisions(t *testing.T) {
	tools := []ResponseTool{
		{Type: "namespace", Name: "one", NamespaceTools: []ResponseTool{{Type: "function", Name: "lookup"}}},
		{Type: "namespace", Name: "two", NamespaceTools: []ResponseTool{{Type: "function", Name: "lookup"}}},
		{Type: "function", Name: "lookup"},
	}
	chat, names, err := convertTools(tools)
	if err != nil {
		t.Fatal(err)
	}
	if len(chat) != 3 || chat[0].Function.Name == chat[1].Function.Name || chat[0].Function.Name == chat[2].Function.Name || chat[1].Function.Name == chat[2].Function.Name {
		t.Fatalf("tool names collided: %#v", chat)
	}
	for i, namespace := range []string{"one", "two"} {
		call, err := names.restoreCall(AssembledToolCall{Name: chat[i].Function.Name})
		if err != nil || call.Namespace != namespace || call.Name != "lookup" {
			t.Fatalf("restored namespace tool = %+v, err=%v", call, err)
		}
	}
}

func TestConvertNamespaceToolsRejectsUnsupportedShapesAndDeferredTools(t *testing.T) {
	for name, tools := range map[string][]ResponseTool{
		"empty namespace":                {{Type: "namespace", Name: "empty"}},
		"unsupported nested custom tool": {{Type: "namespace", Name: "n", NamespaceTools: []ResponseTool{{Type: "custom", Name: "x"}}}},
		"deferred nested tool":           {{Type: "namespace", Name: "n", NamespaceTools: []ResponseTool{{Type: "function", Name: "x", DeferLoading: true}}}},
		"deferred top-level tool":        {{Type: "function", Name: "x", DeferLoading: true}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := convertTools(tools); err == nil {
				t.Fatal("accepted unsupported tool shape")
			}
		})
	}
}

func TestNamespacedFunctionCallContinuationPreservesIdentity(t *testing.T) {
	req := ResponsesRequest{
		Model: "glm-5.3-flash",
		Tools: []ResponseTool{{Type: "namespace", Name: "agents", NamespaceTools: []ResponseTool{{Type: "function", Name: "wait_agent", InputSchema: json.RawMessage(`{"type":"object"}`)}}}},
		Input: json.RawMessage(`[{"type":"function_call","call_id":"call_1","namespace":"agents","name":"wait_agent","arguments":"{}"},{"type":"function_call_output","call_id":"call_1","namespace":"agents","name":"wait_agent","output":"done"}]`),
	}
	chat, names, err := BuildChatRequestWithToolMap(req)
	if err != nil {
		t.Fatal(err)
	}
	if len(chat.Messages) != 2 || len(chat.Messages[0].ToolCalls) != 1 || chat.Messages[0].ToolCalls[0].Function.Name != chat.Tools[0].Function.Name || chat.Messages[0].ToolCalls[0].ID != "call_1" {
		t.Fatalf("assistant history not translated consistently: %#v", chat.Messages)
	}
	if chat.Messages[1].Role != "tool" || chat.Messages[1].ToolCallID != "call_1" || chat.Messages[1].Content != "done" {
		t.Fatalf("tool result linkage changed: %#v", chat.Messages[1])
	}
	if _, err := names.restoreCall(AssembledToolCall{ID: "call_1", Name: chat.Tools[0].Function.Name, Arguments: `{}`}); err != nil {
		t.Fatal(err)
	}
}

func TestConvertWrappedNamespaceFunctionPreservesIdentity(t *testing.T) {
	strict := true
	tools := []ResponseTool{{Type: "namespace", Name: "agents", NamespaceTools: []ResponseTool{{
		Type: "function",
		Function: &ResponseFunc{
			Name:        "wait_agent",
			Description: "Wait for an agent",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}}}`),
			Strict:      &strict,
		},
	}}}}

	chat, names, err := convertTools(tools)
	if err != nil {
		t.Fatal(err)
	}
	if len(chat) != 1 || chat[0].Function.Description != "Wait for an agent" || chat[0].Function.Strict == nil || !*chat[0].Function.Strict {
		t.Fatalf("wrapped namespace function was not normalized: %#v", chat)
	}
	if string(chat[0].Function.Parameters) != `{"type":"object","properties":{"id":{"type":"string"}}}` {
		t.Fatalf("wrapped namespace schema changed: %s", chat[0].Function.Parameters)
	}
	restored, err := names.restoreCall(AssembledToolCall{ID: "call_wrapped", Name: chat[0].Function.Name, Arguments: `{}`})
	if err != nil {
		t.Fatal(err)
	}
	if restored.Namespace != "agents" || restored.Name != "wait_agent" || restored.ID != "call_wrapped" {
		t.Fatalf("wrapped namespace identity changed: %+v", restored)
	}
}
