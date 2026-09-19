// Package agentd speaks the stable Codex App Server JSONL protocol for one
// headless task process. It is deliberately transport-focused: Controller
// persistence and executor lifecycle remain outside the coding harness.
package agentd

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
)

type Event struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

type Client struct {
	reader      *bufio.Scanner
	writer      io.Writer
	nextID      int
	initialized bool
	pending     []Event
}

func NewClient(input io.Reader, output io.Writer) *Client {
	return &Client{reader: bufio.NewScanner(input), writer: output, nextID: 1}
}

func (c *Client) Initialize(ctx context.Context) error {
	if c.initialized {
		return nil
	}
	if _, err := c.call(ctx, "initialize", map[string]any{
		"clientInfo": map[string]string{"name": "codex-homelab-agentd", "version": "0.1.0"},
	}); err != nil {
		return err
	}
	if err := c.notify("initialized", map[string]any{}); err != nil {
		return err
	}
	c.initialized = true
	return nil
}

func (c *Client) StartNewTurn(ctx context.Context, prompt string) (string, []Event, error) {
	if err := c.Initialize(ctx); err != nil {
		return "", nil, err
	}
	threadID, err := c.StartThread(ctx)
	if err != nil {
		return "", nil, err
	}
	if _, err := c.StartTurn(ctx, threadID, prompt); err != nil {
		return "", nil, err
	}
	events, err := c.CollectTurnEvents(ctx)
	return threadID, events, err
}

func (c *Client) StartThread(ctx context.Context) (string, error) {
	if !c.initialized {
		return "", fmt.Errorf("app server client is not initialized")
	}
	result, err := c.call(ctx, "thread/start", map[string]any{})
	if err != nil {
		return "", err
	}
	return nestedID(result, "thread")
}

func (c *Client) ResumeThread(ctx context.Context, threadID string) error {
	if !c.initialized {
		return fmt.Errorf("app server client is not initialized")
	}
	_, err := c.call(ctx, "thread/resume", map[string]any{"threadId": threadID})
	return err
}

func (c *Client) StartTurn(ctx context.Context, threadID, prompt string) (string, error) {
	if !c.initialized {
		return "", fmt.Errorf("app server client is not initialized")
	}
	result, err := c.call(ctx, "turn/start", map[string]any{
		"threadId":       threadID,
		"input":          []map[string]string{{"type": "text", "text": prompt}},
		"approvalPolicy": "never",
	})
	if err != nil {
		return "", err
	}
	return nestedID(result, "turn")
}

func (c *Client) InterruptTurn(ctx context.Context, threadID, turnID string) error {
	if !c.initialized {
		return fmt.Errorf("app server client is not initialized")
	}
	_, err := c.call(ctx, "turn/interrupt", map[string]any{"threadId": threadID, "turnId": turnID})
	return err
}

func (c *Client) CollectTurnEvents(ctx context.Context) ([]Event, error) {
	events := append([]Event(nil), c.pending...)
	c.pending = nil
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !c.reader.Scan() {
			if err := c.reader.Err(); err != nil {
				return nil, fmt.Errorf("read app server event: %w", err)
			}
			return nil, io.EOF
		}
		var message wireMessage
		if err := json.Unmarshal(c.reader.Bytes(), &message); err != nil {
			return nil, fmt.Errorf("decode app server event: %w", err)
		}
		if message.Method == "" {
			continue
		}
		event := Event{Method: message.Method, Params: message.Params}
		events = append(events, event)
		if event.Method == "turn/completed" {
			return events, nil
		}
	}
}

func (c *Client) call(ctx context.Context, method string, params any) (map[string]json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	id := c.nextID
	c.nextID++
	if err := c.write(map[string]any{"id": id, "method": method, "params": params}); err != nil {
		return nil, err
	}
	for {
		if !c.reader.Scan() {
			if err := c.reader.Err(); err != nil {
				return nil, fmt.Errorf("read app server response: %w", err)
			}
			return nil, io.EOF
		}
		var message wireMessage
		if err := json.Unmarshal(c.reader.Bytes(), &message); err != nil {
			return nil, fmt.Errorf("decode app server response: %w", err)
		}
		if message.Method != "" {
			c.pending = append(c.pending, Event{Method: message.Method, Params: message.Params})
			continue
		}
		if message.ID != id {
			continue
		}
		if message.Error != nil {
			return nil, fmt.Errorf("app server %s: %s", method, message.Error.Message)
		}
		var result map[string]json.RawMessage
		if err := json.Unmarshal(message.Result, &result); err != nil {
			return nil, fmt.Errorf("decode app server %s result: %w", method, err)
		}
		return result, nil
	}
}

func (c *Client) notify(method string, params any) error {
	return c.write(map[string]any{"method": method, "params": params})
}

func (c *Client) write(message any) error {
	encoded, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("encode app server request: %w", err)
	}
	if _, err := fmt.Fprintf(c.writer, "%s\n", encoded); err != nil {
		return fmt.Errorf("write app server request: %w", err)
	}
	return nil
}

type wireMessage struct {
	ID     int             `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  *wireError      `json:"error"`
}

type wireError struct {
	Message string `json:"message"`
}

func nestedID(result map[string]json.RawMessage, key string) (string, error) {
	var object struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(result[key], &object); err != nil {
		return "", fmt.Errorf("decode %s result: %w", key, err)
	}
	if object.ID == "" {
		return "", fmt.Errorf("app server returned empty %s ID", key)
	}
	return object.ID, nil
}
