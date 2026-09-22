package muse

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type scriptedClient struct {
	responses []Response
	requests  []Request
	err       error
}

func (c *scriptedClient) CreateResponse(_ context.Context, request Request) (Response, error) {
	c.requests = append(c.requests, request)
	if c.err != nil {
		return Response{}, c.err
	}
	response := c.responses[0]
	c.responses = c.responses[1:]
	return response, nil
}

func TestRunnerExecutesTerminalToolAndContinuesResponse(t *testing.T) {
	workspace := t.TempDir()
	client := &scriptedClient{responses: []Response{
		functionCallResponse("resp-1", "call-1", fmt.Sprintf(`{"command":%q}`, markerCommand())),
		messageResponse("resp-2", "created marker"),
	}}
	runner := NewRunner(client, NewTerminal(workspace), "muse-spark-1.3-contributor")

	result, err := runner.Run(context.Background(), "create the marker")
	if err != nil {
		t.Fatal(err)
	}
	if result.ResponseID != "resp-2" || result.Text != "created marker" {
		t.Fatalf("result = %+v", result)
	}
	if len(client.requests) != 2 || client.requests[1].PreviousResponseID != "resp-1" {
		t.Fatalf("requests = %+v", client.requests)
	}
	if len(client.requests[1].Input) != 1 || client.requests[1].Input[0].Type != "function_call_output" || !strings.Contains(client.requests[1].Input[0].Output, `"exit_code":0`) {
		t.Fatalf("tool output request = %+v", client.requests[1].Input)
	}
	marker, err := os.ReadFile(filepath.Join(workspace, "marker.txt"))
	if err != nil || string(marker) != "P13-E2E" {
		t.Fatalf("marker = %q, err=%v", marker, err)
	}
}

func TestRunnerCanContinueWithFullInputHistory(t *testing.T) {
	workspace := t.TempDir()
	client := &scriptedClient{responses: []Response{
		functionCallResponse("resp-1", "call-1", fmt.Sprintf(`{"command":%q}`, markerCommand())),
		messageResponse("resp-2", "created marker"),
	}}
	runner := NewRunner(client, NewTerminal(workspace), "muse-spark-1.3-contributor")
	runner.ManualContinuation = true

	if _, err := runner.Run(context.Background(), "create the marker"); err != nil {
		t.Fatal(err)
	}
	if len(client.requests) != 2 {
		t.Fatalf("requests = %+v", client.requests)
	}
	continuation := client.requests[1]
	if continuation.PreviousResponseID != "" || len(continuation.Input) != 3 {
		t.Fatalf("continuation = %+v", continuation)
	}
	if continuation.Input[0].Role != "user" || continuation.Input[1].Type != "function_call" || continuation.Input[1].CallID != "call-1" || continuation.Input[2].Type != "function_call_output" || continuation.Input[2].CallID != "call-1" {
		t.Fatalf("continuation input = %+v", continuation.Input)
	}
}

func TestRunnerRejectsUnsupportedToolAndMalformedArguments(t *testing.T) {
	for _, response := range []Response{
		{ID: "resp-1", Status: "requires_action", Output: []json.RawMessage{json.RawMessage(`{"type":"function_call","call_id":"call-1","name":"shell","arguments":"{}"}`)}},
		functionCallResponse("resp-1", "call-1", `{"command":`),
	} {
		runner := NewRunner(&scriptedClient{responses: []Response{response}}, NewTerminal(t.TempDir()), "muse-spark-1.3-contributor")
		if _, err := runner.Run(context.Background(), "do it"); err == nil || !strings.Contains(err.Error(), "terminal") {
			t.Fatalf("response=%+v error=%v", response, err)
		}
	}
}

func TestRunnerPropagatesProviderErrorAndTurnLimit(t *testing.T) {
	providerErr := errors.New("provider unavailable")
	failed := NewRunner(&scriptedClient{err: providerErr}, NewTerminal(t.TempDir()), "muse-spark-1.3-contributor")
	if _, err := failed.Run(context.Background(), "do it"); !errors.Is(err, providerErr) {
		t.Fatalf("provider error = %v", err)
	}
	loop := NewRunner(&scriptedClient{responses: []Response{functionCallResponse("resp-1", "call-1", `{"command":"printf x"}`)}}, NewTerminal(t.TempDir()), "muse-spark-1.3-contributor")
	loop.MaxTurns = 1
	if _, err := loop.Run(context.Background(), "loop"); err == nil || !strings.Contains(err.Error(), "turn limit") {
		t.Fatalf("turn limit error = %v", err)
	}
}

func functionCallResponse(id, callID, arguments string) Response {
	item, _ := json.Marshal(map[string]any{"type": "function_call", "call_id": callID, "name": "terminal", "arguments": arguments})
	return Response{ID: id, Status: "requires_action", Output: []json.RawMessage{item}}
}

func messageResponse(id, text string) Response {
	item, _ := json.Marshal(map[string]any{"type": "message", "content": []map[string]string{{"type": "output_text", "text": text}}})
	return Response{ID: id, Status: "completed", Output: []json.RawMessage{item}}
}
