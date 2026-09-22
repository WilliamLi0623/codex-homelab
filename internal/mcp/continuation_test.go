package mcp

import (
	"context"
	"errors"
	"testing"

	"github.com/WilliamLi0623/codex-homelab/internal/store"
)

type mcpFakeMessageSender struct {
	calls int
	err   error
}

func (f *mcpFakeMessageSender) SendMessageForAttempt(_ context.Context, _, _ string) error {
	f.calls++
	return f.err
}

func TestContinueTaskUsesDurableDeliveryAndDoesNotReplay(t *testing.T) {
	base := newTestServer(t)
	task := submitTestTask(t, base)
	started := call[RetryTaskResult](t, base, "start_attempt", raw(map[string]any{"task_id": task.Task.ID, "profile": "openai-primary"}))
	sender := &mcpFakeMessageSender{}
	server := NewServerWithDispatcherAndMessageSender(base.store, nil, sender)
	arguments := raw(map[string]any{"task_id": task.Task.ID, "attempt_id": started.Attempt.ID, "body": "continue", "idempotency_key": "turn-1"})

	first := call[ContinueTaskResult](t, server, "continue_task", arguments)
	second := call[ContinueTaskResult](t, server, "continue_task", arguments)
	if first.State != store.ContinuationDelivered || second.ID != first.ID || sender.calls != 1 {
		t.Fatalf("first=%+v second=%+v calls=%d, want one delivered continuation", first, second, sender.calls)
	}
}

func TestContinueTaskReportsUnknownWithoutReplaying(t *testing.T) {
	base := newTestServer(t)
	task := submitTestTask(t, base)
	started := call[RetryTaskResult](t, base, "start_attempt", raw(map[string]any{"task_id": task.Task.ID, "profile": "openai-primary"}))
	sender := &mcpFakeMessageSender{err: errors.New("timeout")}
	server := NewServerWithDispatcherAndMessageSender(base.store, nil, sender)
	arguments := raw(map[string]any{"task_id": task.Task.ID, "attempt_id": started.Attempt.ID, "body": "continue", "idempotency_key": "turn-unknown"})
	first := call[ContinueTaskResult](t, server, "continue_task", arguments)
	second := call[ContinueTaskResult](t, server, "continue_task", arguments)
	if first.State != store.ContinuationUnknown || second.State != store.ContinuationUnknown || sender.calls != 1 {
		t.Fatalf("first=%+v second=%+v calls=%d, want one unknown delivery", first, second, sender.calls)
	}
}
