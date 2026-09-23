package responsesbridge

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

const DefaultModel = "glm-5.3-flash"

type ResponsesRequest struct {
	Model        string              `json:"model"`
	Instructions string              `json:"instructions,omitempty"`
	Reasoning    *ResponsesReasoning `json:"reasoning,omitempty"`
	// Store is accepted as a client request hint; the bridge remains stateless
	// and never stores Responses objects or conversation history.
	Store   *bool    `json:"store,omitempty"`
	Include []string `json:"include,omitempty"`
	// PromptCacheKey is accepted as client metadata only. The bridge is stateless
	// and does not use it to persist or route requests.
	PromptCacheKey string `json:"prompt_cache_key,omitempty"`
	// ClientMetadata is accepted as opaque client metadata and is not forwarded,
	// logged, persisted, or used for routing.
	ClientMetadata  json.RawMessage `json:"client_metadata,omitempty"`
	Input           json.RawMessage `json:"input"`
	Tools           []ResponseTool  `json:"tools,omitempty"`
	ToolChoice      json.RawMessage `json:"tool_choice,omitempty"`
	ParallelTools   *bool           `json:"parallel_tool_calls,omitempty"`
	Stream          bool            `json:"stream,omitempty"`
	MaxOutputTokens int             `json:"max_output_tokens,omitempty"`
}

type ResponsesReasoning struct {
	Effort  string `json:"effort,omitempty"`
	Summary string `json:"summary,omitempty"`
}

func (reasoning *ResponsesReasoning) UnmarshalJSON(data []byte) error {
	type responsesReasoning ResponsesReasoning
	var decoded responsesReasoning
	if err := strictDecode(data, &decoded); err != nil {
		return fmt.Errorf("decode Responses reasoning options: %w", err)
	}
	*reasoning = ResponsesReasoning(decoded)
	return nil
}

type ResponseTool struct {
	Type           string          `json:"type"`
	Name           string          `json:"name,omitempty"`
	Description    string          `json:"description,omitempty"`
	Parameters     json.RawMessage `json:"parameters,omitempty"`
	InputSchema    json.RawMessage `json:"inputSchema,omitempty"`
	Strict         *bool           `json:"strict,omitempty"`
	DeferLoading   bool            `json:"deferLoading,omitempty"`
	NamespaceTools []ResponseTool  `json:"tools,omitempty"`
	Function       *ResponseFunc   `json:"function,omitempty"`
}

type ResponseFunc struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	Strict      *bool           `json:"strict,omitempty"`
}

func (tool *ResponseTool) UnmarshalJSON(data []byte) error {
	type responseTool ResponseTool
	var decoded responseTool
	if err := strictDecode(data, &decoded); err != nil {
		return fmt.Errorf("decode Responses tool (%s): %w", jsonObjectShape(data), err)
	}
	*tool = ResponseTool(decoded)
	return nil
}

// jsonObjectShape reports only structural metadata useful for protocol
// diagnostics; it never includes descriptions, schemas, prompts, or values.
func jsonObjectShape(data []byte) string {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return "invalid object"
	}
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	typeName := ""
	if raw := fields["type"]; len(raw) > 0 {
		_ = json.Unmarshal(raw, &typeName)
	}
	shape := fmt.Sprintf("type=%q fields=%v", typeName, keys)
	if raw := fields["tools"]; len(raw) > 0 {
		var nested []json.RawMessage
		if json.Unmarshal(raw, &nested) == nil {
			items := make([]string, 0, len(nested))
			for _, item := range nested {
				var entry map[string]json.RawMessage
				if json.Unmarshal(item, &entry) != nil {
					items = append(items, "invalid")
					continue
				}
				nestedType, nestedName := "", ""
				_ = json.Unmarshal(entry["type"], &nestedType)
				_ = json.Unmarshal(entry["name"], &nestedName)
				if nestedName == "" || !safeToolIdentifier(nestedName) {
					nestedName = "<redacted>"
				}
				entryKeys := make([]string, 0, len(entry))
				for key := range entry {
					entryKeys = append(entryKeys, key)
				}
				sort.Strings(entryKeys)
				items = append(items, fmt.Sprintf("%s:%s%v", nestedType, nestedName, entryKeys))
				if len(items) == 16 {
					break
				}
			}
			shape += fmt.Sprintf(" nested_tools=%v", items)
		}
	}
	return shape
}

func safeToolIdentifier(value string) bool {
	if len(value) > 64 {
		return false
	}
	return strings.Trim(value, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_.-") == ""
}

func (function *ResponseFunc) UnmarshalJSON(data []byte) error {
	type responseFunc ResponseFunc
	var decoded responseFunc
	if err := strictDecode(data, &decoded); err != nil {
		return fmt.Errorf("decode Responses function tool: %w", err)
	}
	*function = ResponseFunc(decoded)
	return nil
}

type responseInputItem struct {
	ID        string          `json:"id,omitempty"`
	Type      string          `json:"type"`
	Role      string          `json:"role,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
	CallID    string          `json:"call_id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Namespace string          `json:"namespace,omitempty"`
	Arguments string          `json:"arguments,omitempty"`
	Output    json.RawMessage `json:"output,omitempty"`
}

type responseContentPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func (item *responseInputItem) UnmarshalJSON(data []byte) error {
	type responseItem responseInputItem
	var decoded responseItem
	if err := strictDecode(data, &decoded); err != nil {
		return fmt.Errorf("decode Responses input item: %w", err)
	}
	*item = responseInputItem(decoded)
	return nil
}

func (part *responseContentPart) UnmarshalJSON(data []byte) error {
	type contentPart responseContentPart
	var decoded contentPart
	if err := strictDecode(data, &decoded); err != nil {
		return fmt.Errorf("decode Responses content part: %w", err)
	}
	*part = responseContentPart(decoded)
	return nil
}

func strictDecode(data []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return fmt.Errorf("expected one JSON value")
		}
		return err
	}
	return nil
}

func validateInputEnvelope(raw json.RawMessage) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '[' {
		return nil
	}
	var items []responseInputItem
	if err := json.Unmarshal(trimmed, &items); err != nil {
		return fmt.Errorf("parse input items: %w", err)
	}
	for _, item := range items {
		content := bytes.TrimSpace(item.Content)
		if len(content) > 0 && content[0] == '[' {
			var parts []responseContentPart
			if err := json.Unmarshal(content, &parts); err != nil {
				return fmt.Errorf("parse input content parts: %w", err)
			}
		}
	}
	return nil
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
