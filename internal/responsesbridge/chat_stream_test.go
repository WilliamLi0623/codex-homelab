package responsesbridge

import (
	"strings"
	"testing"
)

func TestDecodeChatSSEAssemblesFragmentedToolCallAndUnicode(t *testing.T) {
	fixture := string(loadFixture(t, "stream_tool_call.sse"))
	decoded, err := DecodeChatSSE(strings.NewReader(fixture))
	if err != nil {
		t.Fatal(err)
	}
	if len(decoded.ToolCalls) != 1 {
		t.Fatalf("tool calls = %d, want 1", len(decoded.ToolCalls))
	}
	call := decoded.ToolCalls[0]
	if call.ID != "call_unicode_01" || call.Name != "get_test_value" {
		t.Fatalf("identity changed: %#v", call)
	}
	if call.Arguments != `{"input":"你好 🌍"}` {
		t.Fatalf("arguments = %q", call.Arguments)
	}
	if !decoded.Completed {
		t.Fatal("successful upstream tool-call stream must be marked complete")
	}
}

func TestDecodeChatSSERequiresDoneAndRejectsMalformedInput(t *testing.T) {
	if _, err := DecodeChatSSE(strings.NewReader("data: {not-json}\n\n")); err == nil {
		t.Fatal("expected malformed JSON error")
	}
	if _, err := DecodeChatSSE(strings.NewReader("data: {\"choices\":[]}\n\n")); err == nil {
		t.Fatal("expected interrupted stream error")
	}
}

func TestDecodeChatSSEPreservesParallelToolIndexes(t *testing.T) {
	decoded, err := DecodeChatSSE(strings.NewReader(string(loadFixture(t, "stream_parallel_tool_calls.sse"))))
	if err != nil {
		t.Fatal(err)
	}
	if len(decoded.ToolCalls) != 2 || decoded.ToolCalls[0].ID != "call_a" || decoded.ToolCalls[1].ID != "call_b" {
		t.Fatalf("parallel calls = %#v", decoded.ToolCalls)
	}
}

func TestDecodeChatSSEPreservesSparseIndexesInSortedOrder(t *testing.T) {
	input := "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":2,\"id\":\"call_c\",\"function\":{\"name\":\"c\",\"arguments\":\"{}\"}},{\"index\":5,\"id\":\"call_f\",\"function\":{\"name\":\"f\",\"arguments\":\"{}\"}}]}}]}\n\ndata: [DONE]\n\n"
	decoded, err := DecodeChatSSE(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if len(decoded.ToolCalls) != 2 || decoded.ToolCalls[0].ID != "call_c" || decoded.ToolCalls[1].ID != "call_f" {
		t.Fatalf("sparse calls = %#v", decoded.ToolCalls)
	}
}

func TestDecodeChatSSERejectsIncompleteDuplicateAndMissingToolMetadata(t *testing.T) {
	for name, input := range map[string]string{
		"missing id":     "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"name\":\"x\",\"arguments\":\"{}\"}}]}}]}\n\ndata: [DONE]\n\n",
		"duplicate ids":  "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"same\",\"function\":{\"name\":\"x\",\"arguments\":\"{}\"}},{\"index\":1,\"id\":\"same\",\"function\":{\"name\":\"y\",\"arguments\":\"{}\"}}]}}]}\n\ndata: [DONE]\n\n",
		"missing index":  "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"id\":\"c\",\"function\":{\"name\":\"x\",\"arguments\":\"{}\"}}]}}]}\n\ndata: [DONE]\n\n",
		"duplicate done": "data: [DONE]\n\ndata: [DONE]\n\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeChatSSE(strings.NewReader(input)); err == nil {
				t.Fatal("expected stream validation error")
			}
		})
	}
}

func TestDecodeChatSSERejectsInvalidFinalArguments(t *testing.T) {
	input := "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"c\",\"function\":{\"name\":\"x\",\"arguments\":\"not-json\"}}]}}]}\n\ndata: [DONE]\n\n"
	if _, err := DecodeChatSSE(strings.NewReader(input)); err == nil {
		t.Fatal("expected invalid JSON arguments error")
	}
}
