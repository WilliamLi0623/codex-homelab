package responsesbridge

import (
	"encoding/json"
	"fmt"
)

type ChatRequest struct {
	Model             string          `json:"model"`
	Messages          []ChatMessage   `json:"messages"`
	Tools             []ChatTool      `json:"tools,omitempty"`
	ToolChoice        json.RawMessage `json:"tool_choice,omitempty"`
	ParallelToolCalls *bool           `json:"parallel_tool_calls,omitempty"`
	Stream            bool            `json:"stream"`
	MaxTokens         int             `json:"max_tokens,omitempty"`
	ReasoningEffort   string          `json:"reasoning_effort,omitempty"`
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
	chat, _, err := BuildChatRequestWithToolMap(req)
	return chat, err
}

func BuildChatRequestWithToolMap(req ResponsesRequest) (ChatRequest, *ToolNameMap, error) {
	if req.Model == "" {
		return ChatRequest{}, nil, fmt.Errorf("model is required")
	}
	items, err := parseInput(req.Input)
	if err != nil {
		return ChatRequest{}, nil, err
	}
	tools, toolNames, err := convertTools(req.Tools)
	if err != nil {
		return ChatRequest{}, nil, err
	}
	chat := ChatRequest{Model: req.Model, Stream: req.Stream, Tools: tools, MaxTokens: req.MaxOutputTokens}
	if len(req.ToolChoice) != 0 {
		var choice string
		if err := json.Unmarshal(req.ToolChoice, &choice); err != nil {
			return ChatRequest{}, nil, fmt.Errorf("unsupported Responses tool_choice shape")
		}
		switch choice {
		case "auto", "none", "required":
			chat.ToolChoice, _ = json.Marshal(choice)
		default:
			return ChatRequest{}, nil, fmt.Errorf("unsupported Responses tool_choice value %q", choice)
		}
	}
	chat.ParallelToolCalls = req.ParallelTools
	if req.Reasoning != nil {
		chat.ReasoningEffort = req.Reasoning.Effort
	}
	if req.Instructions != "" {
		chat.Messages = append(chat.Messages, ChatMessage{Role: "system", Content: req.Instructions})
	}
	var pending *ChatMessage
	pendingCalls := map[string]toolTarget{}
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
				return ChatRequest{}, nil, err
			}
			chat.Messages = append(chat.Messages, ChatMessage{Role: role, Content: content})
		case "function_call":
			if item.CallID == "" || item.Name == "" || item.Arguments == "" {
				return ChatRequest{}, nil, fmt.Errorf("function_call requires non-empty call_id, name, and arguments")
			}
			if _, exists := pendingCalls[item.CallID]; exists {
				return ChatRequest{}, nil, fmt.Errorf("duplicate function_call call_id %q", item.CallID)
			}
			chatName, err := toolNames.chatName(item.Namespace, item.Name)
			if err != nil {
				return ChatRequest{}, nil, err
			}
			pendingCalls[item.CallID] = toolTarget{Namespace: item.Namespace, Name: item.Name}
			if pending == nil {
				pending = &ChatMessage{Role: "assistant"}
			}
			pending.ToolCalls = append(pending.ToolCalls, ChatToolCall{ID: item.CallID, Type: "function", Function: ChatFunctionCall{Name: chatName, Arguments: item.Arguments}})
		case "function_call_output":
			if item.CallID == "" || item.Output == nil {
				return ChatRequest{}, nil, fmt.Errorf("function_call_output requires non-empty call_id and output")
			}
			previous, exists := pendingCalls[item.CallID]
			if !exists {
				return ChatRequest{}, nil, fmt.Errorf("function_call_output references unknown call_id %q", item.CallID)
			}
			if item.Name != "" && (item.Name != previous.Name || item.Namespace != previous.Namespace) {
				return ChatRequest{}, nil, fmt.Errorf("function_call_output identity does not match call_id %q", item.CallID)
			}
			if _, exists := usedOutputs[item.CallID]; exists {
				return ChatRequest{}, nil, fmt.Errorf("duplicate function_call_output call_id %q", item.CallID)
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
			return ChatRequest{}, nil, fmt.Errorf("unsupported Responses input item type %q", item.Type)
		}
	}
	flush()
	for callID := range pendingCalls {
		if _, ok := usedOutputs[callID]; !ok {
			return ChatRequest{}, nil, fmt.Errorf("function_call %q has no matching function_call_output", callID)
		}
	}
	return chat, toolNames, nil
}

func messageText(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text, nil
	}
	var parts []responseContentPart
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
