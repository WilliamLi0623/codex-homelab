package responsesbridge

import (
	"bytes"
	"encoding/json"
	"fmt"
)

const DefaultModel = "glm-5.3-flash"

type ResponsesRequest struct {
	Model           string          `json:"model"`
	Instructions    string          `json:"instructions,omitempty"`
	Input           json.RawMessage `json:"input"`
	Tools           []ResponseTool  `json:"tools,omitempty"`
	ToolChoice      json.RawMessage `json:"tool_choice,omitempty"`
	ParallelTools   *bool           `json:"parallel_tool_calls,omitempty"`
	Stream          bool            `json:"stream,omitempty"`
	MaxOutputTokens int             `json:"max_output_tokens,omitempty"`
}

type ResponseTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name,omitempty"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	Function    *ResponseFunc   `json:"function,omitempty"`
}

type ResponseFunc struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type responseInputItem struct {
	Type      string          `json:"type"`
	Role      string          `json:"role,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
	CallID    string          `json:"call_id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Arguments string          `json:"arguments,omitempty"`
	Output    json.RawMessage `json:"output,omitempty"`
}

func parseInput(raw json.RawMessage) ([]responseInputItem, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil, nil
	}
	if raw[0] == '"' {
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return nil, fmt.Errorf("parse input text: %w", err)
		}
		return []responseInputItem{{Type: "message", Role: "user", Content: json.RawMessage(fmt.Sprintf(`[{"type":"input_text","text":%s}]`, mustJSON(text)))}}, nil
	}
	if raw[0] != '[' {
		return nil, fmt.Errorf("input must be a string or array")
	}
	var items []responseInputItem
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("parse input items: %w", err)
	}
	return items, nil
}

func mustJSON(v string) string {
	b, _ := json.Marshal(v)
	return string(b)
}
