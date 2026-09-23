package responsesbridge

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"sort"
)

type ChatStreamResult struct {
	Text      string
	ToolCalls []AssembledToolCall
	Usage     *ChatUsage
	Completed bool
}

type ChatDelta struct {
	Text      string
	ToolCalls []ToolCallDelta
	Usage     *ChatUsage
	Done      bool
}

type ToolCallDelta struct {
	Index        int
	IndexPresent bool
	ID           string
	Name         string
	Arguments    string
}

type AssembledToolCall struct {
	ID        string
	Name      string
	Arguments string
}

type ChatUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// StreamChatSSE decodes one upstream SSE frame at a time without buffering text.
// It delays the terminal callback until the stream has exactly one [DONE] and
// all tool calls have complete, unique metadata.
func StreamChatSSE(r io.Reader, onDelta func(ChatDelta) error) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 4096), 2<<20)
	seenDone := false
	assembled := map[int]*AssembledToolCall{}
	idIndexes := map[string]int{}
	var lastUsage *ChatUsage
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" || line[0] == ':' {
			continue
		}
		if len(line) < 6 || line[:5] != "data:" {
			continue
		}
		data := line[5:]
		for len(data) > 0 && data[0] == ' ' {
			data = data[1:]
		}
		if data == "[DONE]" {
			if seenDone {
				return fmt.Errorf("duplicate [DONE] marker")
			}
			seenDone = true
			continue
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content   string `json:"content"`
					ToolCalls []struct {
						Index    *int   `json:"index"`
						ID       string `json:"id"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
			Usage *ChatUsage `json:"usage"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return fmt.Errorf("decode upstream SSE: %w", err)
		}
		if chunk.Usage != nil {
			lastUsage = chunk.Usage
		}
		delta := ChatDelta{Usage: chunk.Usage}
		for _, choice := range chunk.Choices {
			delta.Text += choice.Delta.Content
			for _, call := range choice.Delta.ToolCalls {
				if call.Index == nil {
					return fmt.Errorf("tool call has missing index")
				}
				index := *call.Index
				item := assembled[index]
				if item == nil {
					item = &AssembledToolCall{}
					assembled[index] = item
				}
				if call.ID != "" {
					if item.ID != "" && item.ID != call.ID {
						return fmt.Errorf("tool call index %d has conflicting IDs", index)
					}
					if previous, exists := idIndexes[call.ID]; exists && previous != index {
						return fmt.Errorf("duplicate tool call ID %q", call.ID)
					}
					idIndexes[call.ID] = index
					item.ID = call.ID
				}
				item.Name += call.Function.Name
				item.Arguments += call.Function.Arguments
				delta.ToolCalls = append(delta.ToolCalls, ToolCallDelta{Index: index, IndexPresent: true, ID: call.ID, Name: call.Function.Name, Arguments: call.Function.Arguments})
			}
		}
		if delta.Text != "" || len(delta.ToolCalls) != 0 || delta.Usage != nil {
			if err := onDelta(delta); err != nil {
				return err
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read upstream SSE: %w", err)
	}
	if !seenDone {
		return fmt.Errorf("upstream SSE ended before [DONE]")
	}
	indexes := sortedCallIndexes(assembled)
	for _, index := range indexes {
		call := assembled[index]
		if call.ID == "" || call.Name == "" || call.Arguments == "" {
			return fmt.Errorf("tool call index %d is incomplete", index)
		}
		if !json.Valid([]byte(call.Arguments)) {
			return fmt.Errorf("tool call index %d has invalid JSON arguments", index)
		}
	}
	if err := onDelta(ChatDelta{Usage: lastUsage, Done: true}); err != nil {
		return err
	}
	return nil
}

func DecodeChatSSE(r io.Reader) (ChatStreamResult, error) {
	result := ChatStreamResult{}
	assembled := map[int]*AssembledToolCall{}
	err := StreamChatSSE(r, func(delta ChatDelta) error {
		result.Text += delta.Text
		if delta.Usage != nil {
			result.Usage = delta.Usage
		}
		for _, call := range delta.ToolCalls {
			item := assembled[call.Index]
			if item == nil {
				item = &AssembledToolCall{}
				assembled[call.Index] = item
			}
			if call.ID != "" {
				item.ID = call.ID
			}
			item.Name += call.Name
			item.Arguments += call.Arguments
		}
		if delta.Done {
			result.Completed = true
		}
		return nil
	})
	if err != nil {
		return ChatStreamResult{}, err
	}
	indexes := make([]int, 0, len(assembled))
	for index := range assembled {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	for _, index := range indexes {
		result.ToolCalls = append(result.ToolCalls, *assembled[index])
	}
	return result, nil
}
