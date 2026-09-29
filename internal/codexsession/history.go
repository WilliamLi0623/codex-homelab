package codexsession

import (
	"context"
	"encoding/json"
	"errors"
	"unicode/utf8"
)

const (
	maxHistoryTurns       = 20
	maxHistoryItemsTurn   = 100
	maxHistoryMessages    = 200
	maxHistoryMessageSize = 32 << 10
	maxHistoryTotalSize   = 512 << 10
)

type HistoryMessage struct {
	ID        string `json:"id"`
	Role      string `json:"role"`
	Text      string `json:"text"`
	Truncated bool   `json:"truncated,omitempty"`
}

type SessionHistory struct {
	Messages  []HistoryMessage `json:"messages"`
	Truncated bool             `json:"truncated"`
}

func visibleHistoryItem(raw json.RawMessage) (HistoryMessage, bool) {
	var item struct {
		ID      string `json:"id"`
		Type    string `json:"type"`
		Text    string `json:"text"`
		Phase   string `json:"phase"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if json.Unmarshal(raw, &item) != nil || item.ID == "" {
		return HistoryMessage{}, false
	}
	message := HistoryMessage{ID: item.ID}
	switch item.Type {
	case "userMessage":
		message.Role = "user"
		for _, content := range item.Content {
			if content.Type == "text" {
				message.Text += content.Text
			}
		}
	case "agentMessage":
		if item.Phase != "" && item.Phase != "commentary" && item.Phase != "final_answer" {
			return HistoryMessage{}, false
		}
		message.Role = "assistant"
		message.Text = item.Text
	default:
		return HistoryMessage{}, false
	}
	if message.Text == "" {
		return HistoryMessage{}, false
	}
	message.Text, message.Truncated = truncateHistoryText(message.Text, maxHistoryMessageSize)
	return message, true
}

func truncateHistoryText(value string, limit int) (string, bool) {
	if len(value) <= limit {
		return value, false
	}
	end := limit
	for end > 0 && !utf8.RuneStart(value[end]) {
		end--
	}
	return value[:end], true
}

func fitHistoryMessage(message HistoryMessage, limit int) (HistoryMessage, bool) {
	if limit <= 0 {
		return HistoryMessage{}, false
	}
	if len(message.Text) > limit {
		message.Text, message.Truncated = truncateHistoryText(message.Text, limit)
	}
	if message.Text == "" {
		return HistoryMessage{}, false
	}
	return message, true
}

func (m *SessionManager) History(ctx context.Context, threadID string) (SessionHistory, error) {
	if m.Get(threadID) == nil {
		return SessionHistory{}, errors.New("thread is not registered with this local session manager")
	}
	turns, err := m.client.ListTurns(ctx, threadID, maxHistoryTurns, "")
	if err != nil {
		return SessionHistory{}, err
	}
	type turnRecord struct {
		ID string `json:"id"`
	}
	groups := make([][]HistoryMessage, 0, len(turns.Items))
	textBytes := 0
	truncated := turns.NextCursor != ""
	for _, rawTurn := range turns.Items {
		var turn turnRecord
		if err := json.Unmarshal(rawTurn, &turn); err != nil || turn.ID == "" {
			return SessionHistory{}, errors.New("App Server returned malformed turn history")
		}
		page, err := m.client.ListItems(ctx, threadID, turn.ID, maxHistoryItemsTurn, "")
		if err != nil {
			return SessionHistory{}, err
		}
		if page.NextCursor != "" {
			truncated = true
		}
		group := make([]HistoryMessage, 0, len(page.Items))
		for _, rawItem := range page.Items {
			message, ok := visibleHistoryItem(rawItem)
			if !ok {
				continue
			}
			remaining := maxHistoryTotalSize - textBytes
			if remaining <= 0 {
				truncated = true
				break
			}
			message, ok = fitHistoryMessage(message, remaining)
			if !ok {
				truncated = true
				textBytes = maxHistoryTotalSize
				break
			}
			if message.Truncated {
				truncated = true
			}
			textBytes += len(message.Text)
			group = append(group, message)
			if len(group) >= maxHistoryMessages || textBytes >= maxHistoryTotalSize {
				truncated = true
				break
			}
		}
		for left, right := 0, len(group)-1; left < right; left, right = left+1, right-1 {
			group[left], group[right] = group[right], group[left]
		}
		if len(group) > 0 {
			groups = append(groups, group)
		}
		if len(group) >= maxHistoryMessages || textBytes >= maxHistoryTotalSize {
			break
		}
	}
	messages := make([]HistoryMessage, 0, min(maxHistoryMessages, maxHistoryTurns*2))
	for groupIndex := len(groups) - 1; groupIndex >= 0; groupIndex-- {
		messages = append(messages, groups[groupIndex]...)
	}
	if len(messages) > maxHistoryMessages {
		messages = messages[len(messages)-maxHistoryMessages:]
		truncated = true
	}
	return SessionHistory{Messages: messages, Truncated: truncated}, nil
}
