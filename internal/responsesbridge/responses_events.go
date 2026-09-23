package responsesbridge

import (
	"encoding/json"
	"fmt"
	"io"
	"sync/atomic"
)

var idCounter uint64

type responseUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

func nextID(prefix string) string {
	return fmt.Sprintf("%s_%x", prefix, atomic.AddUint64(&idCounter, 1))
}

func usageForResponse(usage *ChatUsage) *responseUsage {
	if usage == nil {
		return nil
	}
	return &responseUsage{InputTokens: usage.PromptTokens, OutputTokens: usage.CompletionTokens, TotalTokens: usage.TotalTokens}
}

func responseObject(id, status string, output []any, usage *ChatUsage) map[string]any {
	if output == nil {
		output = []any{}
	}
	response := map[string]any{"id": id, "object": "response", "status": status, "output": output}
	if converted := usageForResponse(usage); converted != nil {
		response["usage"] = converted
	}
	return response
}

func messageOutputItem(id, text string) map[string]any {
	return map[string]any{"type": "message", "id": id, "status": "completed", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": text, "annotations": []any{}}}}
}

func functionOutputItem(id string, call AssembledToolCall) map[string]any {
	item := map[string]any{"type": "function_call", "id": id, "status": "completed", "call_id": call.ID, "name": call.Name, "arguments": call.Arguments}
	if call.Namespace != "" {
		item["namespace"] = call.Namespace
	}
	return item
}

func writeEvent(w io.Writer, event string, data any) error {
	b, err := json.Marshal(data)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
	return err
}
