package muse

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"golang.org/x/net/http2"
)

type chatRequest struct {
	Model      string        `json:"model"`
	Messages   []chatMessage `json:"messages"`
	Tools      []chatTool    `json:"tools,omitempty"`
	ToolChoice string        `json:"tool_choice,omitempty"`
	Stream     bool          `json:"stream"`
}

type chatMessage struct {
	Role       string         `json:"role"`
	Content    *string        `json:"content"`
	ToolCalls  []chatToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

type chatTool struct {
	Type     string           `json:"type"`
	Function chatToolFunction `json:"function"`
}

type chatToolFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters"`
}

type chatToolCall struct {
	ID       string               `json:"id"`
	Type     string               `json:"type"`
	Function chatToolCallFunction `json:"function"`
}

type chatToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type chatResponse struct {
	ID      string `json:"id"`
	Choices []struct {
		FinishReason string `json:"finish_reason"`
		Message      struct {
			Content   *string        `json:"content"`
			ToolCalls []chatToolCall `json:"tool_calls"`
		} `json:"message"`
	} `json:"choices"`
}

// ChatHTTPClient presents an OpenAI-compatible Chat Completions endpoint as
// the ResponsesClient used by the bounded Muse runner.
type ChatHTTPClient struct {
	BaseURL string
	APIKey  string
	HTTP    *http.Client
}

func NewChatHTTPClient(baseURL, apiKey string, httpClient *http.Client) *ChatHTTPClient {
	if httpClient == nil {
		httpClient = &http.Client{Transport: &http2.Transport{}}
	}
	return &ChatHTTPClient{BaseURL: strings.TrimRight(baseURL, "/"), APIKey: strings.TrimSpace(apiKey), HTTP: httpClient}
}

func (c *ChatHTTPClient) CreateResponse(ctx context.Context, request Request) (Response, error) {
	if c == nil || strings.TrimSpace(c.BaseURL) == "" {
		return Response{}, errors.New("Muse Chat base URL is required")
	}
	if strings.TrimSpace(c.APIKey) == "" {
		return Response{}, errors.New("Muse Chat API key is required")
	}
	chat := chatRequest{
		Model:      request.Model,
		Messages:   inputToChatMessages(request.Input),
		Tools:      toolsToChatTools(request.Tools),
		ToolChoice: toolChoiceForRequest(request),
		Stream:     false,
	}
	body, err := json.Marshal(chat)
	if err != nil {
		return Response{}, fmt.Errorf("encode Muse Chat request: %w", err)
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, chatEndpoint(c.BaseURL), bytes.NewReader(body))
	if err != nil {
		return Response{}, fmt.Errorf("create Muse Chat request: %w", err)
	}
	httpRequest.Header.Set("Authorization", "Bearer "+c.APIKey)
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Accept", "application/json")
	response, err := c.HTTP.Do(httpRequest)
	if err != nil {
		return Response{}, fmt.Errorf("send Muse Chat request: %w", err)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return Response{}, fmt.Errorf("read Muse Chat response: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		detail := strings.TrimSpace(string(responseBody))
		if len(detail) > 512 {
			detail = detail[:512] + "..."
		}
		if detail == "" {
			return Response{}, fmt.Errorf("Muse Chat provider returned status %d", response.StatusCode)
		}
		return Response{}, fmt.Errorf("Muse Chat provider returned status %d: %s", response.StatusCode, detail)
	}
	return decodeChatResponse(responseBody)
}

func toolChoiceForRequest(request Request) string {
	if len(request.Tools) == 0 {
		return ""
	}
	for _, item := range request.Input {
		if item.Type == "function_call_output" {
			return ""
		}
	}
	return "required"
}

func chatEndpoint(baseURL string) string {
	if strings.HasSuffix(strings.TrimRight(baseURL, "/"), "/v1") {
		return strings.TrimRight(baseURL, "/") + "/chat/completions"
	}
	return strings.TrimRight(baseURL, "/") + "/v1/chat/completions"
}

func inputToChatMessages(input []InputItem) []chatMessage {
	messages := make([]chatMessage, 0, len(input))
	var pendingCalls []chatToolCall
	flushCalls := func() {
		if len(pendingCalls) == 0 {
			return
		}
		messages = append(messages, chatMessage{Role: "assistant", ToolCalls: pendingCalls})
		pendingCalls = nil
	}
	for _, item := range input {
		switch item.Type {
		case "function_call":
			pendingCalls = append(pendingCalls, chatToolCall{ID: item.CallID, Type: "function", Function: chatToolCallFunction{Name: item.Name, Arguments: item.Arguments}})
		case "function_call_output":
			flushCalls()
			content := item.Output
			messages = append(messages, chatMessage{Role: "tool", Content: &content, ToolCallID: item.CallID})
		default:
			flushCalls()
			content := item.Text
			messages = append(messages, chatMessage{Role: item.Role, Content: &content})
		}
	}
	flushCalls()
	return messages
}

func toolsToChatTools(tools []Tool) []chatTool {
	converted := make([]chatTool, 0, len(tools))
	for _, tool := range tools {
		converted = append(converted, chatTool{Type: tool.Type, Function: chatToolFunction{Name: tool.Name, Description: tool.Description, Parameters: tool.Parameters}})
	}
	return converted
}

func decodeChatResponse(data []byte) (Response, error) {
	var response chatResponse
	if err := json.Unmarshal(data, &response); err != nil {
		return Response{}, fmt.Errorf("decode Muse Chat response: %w", err)
	}
	if response.ID == "" {
		return Response{}, errors.New("Muse Chat response ID is missing")
	}
	if len(response.Choices) == 0 {
		return Response{}, errors.New("Muse Chat response choices are missing")
	}
	choice := response.Choices[0]
	result := Response{ID: response.ID, Status: "completed"}
	for _, call := range choice.Message.ToolCalls {
		raw, err := json.Marshal(map[string]any{
			"type":      "function_call",
			"call_id":   call.ID,
			"name":      call.Function.Name,
			"arguments": call.Function.Arguments,
		})
		if err != nil {
			return Response{}, fmt.Errorf("encode Muse Chat tool call: %w", err)
		}
		result.Output = append(result.Output, raw)
	}
	if len(choice.Message.ToolCalls) > 0 {
		result.Status = "requires_action"
		return result, nil
	}
	if choice.Message.Content != nil {
		raw, err := json.Marshal(map[string]any{
			"type": "message",
			"content": []map[string]string{{
				"type": "output_text",
				"text": *choice.Message.Content,
			}},
		})
		if err != nil {
			return Response{}, fmt.Errorf("encode Muse Chat final message: %w", err)
		}
		result.Output = append(result.Output, raw)
	}
	return result, nil
}
