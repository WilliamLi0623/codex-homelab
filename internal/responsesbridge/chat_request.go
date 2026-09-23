package responsesbridge

import (
	"encoding/json"
	"fmt"
)

type ChatRequest struct {
	Model     string        `json:"model"`
	Messages  []ChatMessage `json:"messages"`
	Tools     []ChatTool    `json:"tools,omitempty"`
	Stream    bool          `json:"stream"`
	MaxTokens int           `json:"max_tokens,omitempty"`
}

type ChatMessage struct {
	Role       string         `json:"role"`
	Content    string         `json:"content,omitempty"`
	ToolCalls  []ChatToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

type ChatToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function ChatFunctionCall `json:"function"`
}

type ChatFunctionCall struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

func BuildChatRequest(req ResponsesRequest) (ChatRequest, error) {
	if req.Model == "" {
		return ChatRequest{}, fmt.Errorf("model is required")
	}
	if len(req.ToolChoice) != 0 || req.ParallelTools != nil {
		return ChatRequest{}, fmt.Errorf("tool_choice and parallel_tool_calls are unsupported by this bridge")
	}
	items, err := parseInput(req.Input)
	if err != nil {
		return ChatRequest{}, err
	}
	tools, err := convertTools(req.Tools)
	if err != nil {
		return ChatRequest{}, err
	}
	chat := ChatRequest{Model: req.Model, Stream: req.Stream, Tools: tools, MaxTokens: req.MaxOutputTokens}
	if req.Instructions != "" {
		chat.Messages = append(chat.Messages, ChatMessage{Role: "system", Content: req.Instructions})
	}
	var pending *ChatMessage
	pendingCalls := map[string]struct{}{}
	usedOutputs := map[string]struct{}{}
	flush := func() {
		if pending != nil {
			chat.Messages = append(chat.Messages, *pending)
			pending = nil
		}
	}
	for _, item := range items {
		switch item.Type {
		case "message", "input_message":
			flush()
			role := item.Role
			if role == "" {
				role = "user"
			}
			content, err := messageText(item.Content)
			if err != nil {
				return ChatRequest{}, err
			}
			chat.Messages = append(chat.Messages, ChatMessage{Role: role, Content: content})
		case "function_call":
			if item.CallID == "" || item.Name == "" || item.Arguments == "" {
				return ChatRequest{}, fmt.Errorf("function_call requires non-empty call_id, name, and arguments")
			}
			if _, exists := pendingCalls[item.CallID]; exists {
				return ChatRequest{}, fmt.Errorf("duplicate function_call call_id %q", item.CallID)
			}
			pendingCalls[item.CallID] = struct{}{}
			if pending == nil {
				pending = &ChatMessage{Role: "assistant"}
			}
			pending.ToolCalls = append(pending.ToolCalls, ChatToolCall{ID: item.CallID, Type: "function", Function: ChatFunctionCall{Name: item.Name, Arguments: item.Arguments}})
		case "function_call_output":
			if item.CallID == "" || item.Output == nil {
				return ChatRequest{}, fmt.Errorf("function_call_output requires non-empty call_id and output")
			}
			if _, exists := pendingCalls[item.CallID]; !exists {
				return ChatRequest{}, fmt.Errorf("function_call_output references unknown call_id %q", item.CallID)
			}
			if _, exists := usedOutputs[item.CallID]; exists {
				return ChatRequest{}, fmt.Errorf("duplicate function_call_output call_id %q", item.CallID)
			}
			usedOutputs[item.CallID] = struct{}{}
			flush()
			output := string(item.Output)
			var outputText string
			if err := json.Unmarshal(item.Output, &outputText); err == nil {
				output = outputText
			}
			chat.Messages = append(chat.Messages, ChatMessage{Role: "tool", ToolCallID: item.CallID, Content: output})
		default:
			return ChatRequest{}, fmt.Errorf("unsupported Responses input item type %q", item.Type)
		}
	}
	flush()
	for callID := range pendingCalls {
		if _, ok := usedOutputs[callID]; !ok {
			return ChatRequest{}, fmt.Errorf("function_call %q has no matching function_call_output", callID)
		}
	}
	return chat, nil
}

func messageText(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text, nil
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &parts); err != nil {
		return "", fmt.Errorf("parse message content: %w", err)
	}
	var out string
	for _, part := range parts {
		if part.Type != "input_text" && part.Type != "output_text" && part.Type != "text" {
			return "", fmt.Errorf("unsupported message content type %q", part.Type)
		}
		out += part.Text
	}
	return out, nil
}
