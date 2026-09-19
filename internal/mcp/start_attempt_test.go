package mcp

import (
	"testing"

	"github.com/WilliamLi0623/codex-homelab/internal/domain"
)

func TestStartAttemptCreatesInitialMCPAttempt(t *testing.T) {
	server := newTestServer(t)
	created := submitTestTask(t, server)
	started := call[RetryTaskResult](t, server, "start_attempt", raw(map[string]any{"task_id": created.Task.ID, "profile": "openai-primary"}))
	if started.Task.State != string(domain.TaskPlanned) || started.Attempt.Number != 1 || started.Attempt.State != string(domain.AttemptCreated) {
		t.Fatalf("started=%+v", started)
	}
}
