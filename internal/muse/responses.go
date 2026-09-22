package muse

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

type Request struct {
	Model              string      `json:"model"`
	Input              []InputItem `json:"input"`
	PreviousResponseID string      `json:"previous_response_id,omitempty"`
	Tools              []Tool      `json:"tools,omitempty"`
	Store              bool        `json:"store"`
	Reasoning          *Reasoning  `json:"reasoning,omitempty"`
	ReasoningEffort    string      `json:"-"`
}

type Reasoning struct {
	Effort string `json:"effort,omitempty"`
}

type InputItem struct {
	Role      string
	Text      string
	Type      string
	CallID    string
	Output    string
	Name      string
	Arguments string
}

func (i InputItem) MarshalJSON() ([]byte, error) {
	if i.Type == "function_call" {
		return json.Marshal(struct {
			Type      string `json:"type"`
			CallID    string `json:"call_id"`
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		}{Type: i.Type, CallID: i.CallID, Name: i.Name, Arguments: i.Arguments})
	}
	if i.Type == "function_call_output" {
		return json.Marshal(struct {
			Type   string `json:"type"`
			CallID string `json:"call_id"`
			Output string `json:"output"`
		}{Type: i.Type, CallID: i.CallID, Output: i.Output})
	}
	return json.Marshal(struct {
		Role    string `json:"role"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}{Role: i.Role, Content: []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}{{Type: "input_text", Text: i.Text}}})
}

type Tool struct {
	Type        string         `json:"type"`
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters"`
}

type Response struct {
	ID     string            `json:"id"`
	Status string            `json:"status,omitempty"`
	Output []json.RawMessage `json:"output"`
}

type ToolCall struct {
	CallID    string
	Name      string
	Arguments string
}

func DecodeResponse(data []byte) (Response, error) {
	var response Response
	if err := json.Unmarshal(data, &response); err != nil {
		return Response{}, fmt.Errorf("decode Muse Responses response: %w", err)
	}
	if response.ID == "" {
		return Response{}, errors.New("Muse Responses response ID is missing")
	}
	return response, nil
}

func (r Response) ToolCalls() []ToolCall {
	calls := make([]ToolCall, 0)
	for _, raw := range r.Output {
		var item struct {
			Type      string `json:"type"`
			CallID    string `json:"call_id"`
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		}
		if json.Unmarshal(raw, &item) == nil && item.Type == "function_call" && item.CallID != "" && item.Name != "" {
			calls = append(calls, ToolCall{CallID: item.CallID, Name: item.Name, Arguments: item.Arguments})
		}
	}
	return calls
}

func (r Response) FinalText() string {
	var text bytes.Buffer
	for _, raw := range r.Output {
		var item struct {
			Type    string `json:"type"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		}
		if json.Unmarshal(raw, &item) != nil || item.Type != "message" {
			continue
		}
		for _, content := range item.Content {
			if content.Type == "output_text" {
				text.WriteString(content.Text)
			}
		}
	}
	return text.String()
}
