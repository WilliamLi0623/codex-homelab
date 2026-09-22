package muse

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

type ResponsesClient interface {
	CreateResponse(context.Context, Request) (Response, error)
}

type Event struct {
	Method string
	Params json.RawMessage
}

type RunResult struct {
	ResponseID string
	Text       string
	Events     []Event
}

type Runner struct {
	Client             ResponsesClient
	Terminal           *Terminal
	Model              string
	ReasoningEffort    string
	MaxTurns           int
	ManualContinuation bool
}

func NewRunner(client ResponsesClient, terminal *Terminal, model string) *Runner {
	return &Runner{Client: client, Terminal: terminal, Model: model, MaxTurns: 8}
}

func (r *Runner) Run(ctx context.Context, prompt string) (RunResult, error) {
	if r == nil || r.Client == nil {
		return RunResult{}, errors.New("Muse Responses client is required")
	}
	if r.Terminal == nil {
		return RunResult{}, errors.New("Muse terminal is required")
	}
	if strings.TrimSpace(r.Model) == "" {
		return RunResult{}, errors.New("Muse model is required")
	}
	if strings.TrimSpace(prompt) == "" {
		return RunResult{}, errors.New("Muse prompt is required")
	}
	maxTurns := r.MaxTurns
	if maxTurns <= 0 {
		maxTurns = 8
	}
	request := Request{
		Model:           r.Model,
		Input:           []InputItem{{Role: "user", Text: prompt}},
		Tools:           []Tool{terminalTool()},
		Store:           false,
		Reasoning:       reasoningConfig(r.ReasoningEffort),
		ReasoningEffort: r.ReasoningEffort,
	}
	history := append([]InputItem(nil), request.Input...)
	result := RunResult{}
	for turn := 0; turn < maxTurns; turn++ {
		response, err := r.Client.CreateResponse(ctx, request)
		if err != nil {
			return RunResult{}, fmt.Errorf("Muse Responses request: %w", err)
		}
		result.ResponseID = response.ID
		result.Events = append(result.Events, event("muse.response", map[string]any{"id": response.ID, "status": response.Status})...)
		calls := response.ToolCalls()
		if len(calls) == 0 {
			if response.Status != "" && response.Status != "completed" {
				return RunResult{}, fmt.Errorf("Muse response completed without terminal call: status=%s", response.Status)
			}
			result.Text = response.FinalText()
			result.Events = append(result.Events, event("muse.completed", map[string]any{"id": response.ID, "text": result.Text})...)
			return result, nil
		}
		outputs := make([]InputItem, 0, len(calls))
		for _, call := range calls {
			if call.Name != "terminal" {
				return RunResult{}, fmt.Errorf("unsupported Muse tool %q; only terminal is allowed", call.Name)
			}
			var arguments struct {
				Command string `json:"command"`
			}
			if err := json.Unmarshal([]byte(call.Arguments), &arguments); err != nil {
				return RunResult{}, fmt.Errorf("decode terminal tool arguments: %w", err)
			}
			if strings.TrimSpace(arguments.Command) == "" {
				return RunResult{}, errors.New("terminal tool command is required")
			}
			toolResult, err := r.Terminal.Run(ctx, arguments.Command)
			if err != nil {
				toolResult = ToolResult{ExitCode: 1, Stderr: err.Error()}
			}
			encoded, marshalErr := json.Marshal(toolResult)
			if marshalErr != nil {
				return RunResult{}, fmt.Errorf("encode terminal tool result: %w", marshalErr)
			}
			outputs = append(outputs, InputItem{Type: "function_call_output", CallID: call.CallID, Output: string(encoded)})
			result.Events = append(result.Events, event("muse.tool_call", map[string]any{"call_id": call.CallID, "name": call.Name})...)
			result.Events = append(result.Events, event("muse.tool_result", toolResult)...)
		}
		if r.ManualContinuation {
			for _, call := range calls {
				history = append(history, InputItem{Type: "function_call", CallID: call.CallID, Name: call.Name, Arguments: call.Arguments})
			}
			history = append(history, outputs...)
			request = Request{Model: r.Model, Input: history, Tools: []Tool{terminalTool()}, Store: false, Reasoning: reasoningConfig(r.ReasoningEffort), ReasoningEffort: r.ReasoningEffort}
			continue
		}
		request = Request{Model: r.Model, Input: outputs, PreviousResponseID: response.ID, Tools: []Tool{terminalTool()}, Store: false, Reasoning: reasoningConfig(r.ReasoningEffort), ReasoningEffort: r.ReasoningEffort}
	}
	return RunResult{}, fmt.Errorf("Muse Responses turn limit reached: %d", maxTurns)
}

func reasoningConfig(effort string) *Reasoning {
	if strings.TrimSpace(effort) == "" {
		return nil
	}
	return &Reasoning{Effort: effort}
}

func terminalTool() Tool {
	return Tool{
		Type:        "function",
		Name:        "terminal",
		Description: "Run one bounded shell command in the attempt workspace.",
		Parameters: map[string]any{
			"type":                 "object",
			"properties":           map[string]any{"command": map[string]any{"type": "string"}},
			"required":             []string{"command"},
			"additionalProperties": false,
		},
	}
}

func event(method string, value any) []Event {
	params, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	return []Event{{Method: method, Params: params}}
}
