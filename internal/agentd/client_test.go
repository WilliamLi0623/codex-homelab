package agentd

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestClientInitializesThenStartsThreadAndTurn(t *testing.T) {
	input := strings.NewReader(strings.Join([]string{
		`{"id":1,"result":{}}`,
		`{"id":2,"result":{"thread":{"id":"thr-1"}}}`,
		`{"method":"item/agentMessage/delta","params":{"threadId":"thr-1","delta":"working"}}`,
		`{"id":3,"result":{"turn":{"id":"turn-1","status":"inProgress"}}}`,
		`{"method":"turn/completed","params":{"threadId":"thr-1","turn":{"id":"turn-1","status":"completed"}}}`,
	}, "\n") + "\n")
	var output bytes.Buffer
	client := NewClient(input, &output)

	threadID, events, err := client.StartNewTurn(context.Background(), "Fix the tests")
	if err != nil {
		t.Fatalf("StartNewTurn() error = %v", err)
	}
	if threadID != "thr-1" {
		t.Fatalf("thread ID = %q, want thr-1", threadID)
	}
	if len(events) != 2 || events[0].Method != "item/agentMessage/delta" || events[1].Method != "turn/completed" {
		t.Fatalf("events = %+v, want streamed item and completion", events)
	}
	for _, method := range []string{`"method":"initialize"`, `"method":"initialized"`, `"method":"thread/start"`, `"method":"turn/start"`} {
		if !strings.Contains(output.String(), method) {
			t.Fatalf("outbound messages missing %s: %s", method, output.String())
		}
	}
}

func TestClientResumesThreadAndInterruptsTurn(t *testing.T) {
	input := strings.NewReader(strings.Join([]string{
		`{"id":1,"result":{}}`,
		`{"id":2,"result":{"thread":{"id":"thr-1"}}}`,
		`{"id":3,"result":{"turn":{"id":"turn-1","status":"inProgress"}}}`,
		`{"id":4,"result":{}}`,
	}, "\n") + "\n")
	var output bytes.Buffer
	client := NewClient(input, &output)

	if err := client.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	if err := client.ResumeThread(context.Background(), "thr-1"); err != nil {
		t.Fatalf("ResumeThread() error = %v", err)
	}
	turnID, err := client.StartTurn(context.Background(), "thr-1", "Continue")
	if err != nil {
		t.Fatalf("StartTurn() error = %v", err)
	}
	if err := client.InterruptTurn(context.Background(), "thr-1", turnID); err != nil {
		t.Fatalf("InterruptTurn() error = %v", err)
	}
	for _, method := range []string{`"method":"thread/resume"`, `"method":"turn/interrupt"`} {
		if !strings.Contains(output.String(), method) {
			t.Fatalf("outbound messages missing %s: %s", method, output.String())
		}
	}
}
