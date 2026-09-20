package main

import (
	"context"
	"testing"

	"github.com/WilliamLi0623/codex-homelab/internal/agentd"
)

type fakeRunner struct {
	calls []string
}

func (f *fakeRunner) StartNewTurn(context.Context, string) (string, []agentd.Event, error) {
	f.calls = append(f.calls, "start")
	return "thread-1", []agentd.Event{{Method: "turn/completed"}}, nil
}

func (f *fakeRunner) ResumeThread(context.Context, string) error {
	f.calls = append(f.calls, "resume")
	return nil
}

func (f *fakeRunner) StartTurn(context.Context, string, string) (string, error) {
	f.calls = append(f.calls, "turn")
	return "turn-2", nil
}

func (f *fakeRunner) CollectTurnEvents(context.Context) ([]agentd.Event, error) {
	f.calls = append(f.calls, "collect")
	return []agentd.Event{{Method: "turn/completed"}}, nil
}

func TestSessionFollowUpResumesSameThread(t *testing.T) {
	runner := &fakeRunner{}
	session := newSession(runner)

	first, err := session.run(context.Background(), request{Prompt: "first"})
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	second, err := session.run(context.Background(), request{Prompt: "follow-up"})
	if err != nil {
		t.Fatalf("follow-up run: %v", err)
	}
	if first.ThreadID != "thread-1" || second.ThreadID != first.ThreadID {
		t.Fatalf("thread IDs = %q, %q; want same thread", first.ThreadID, second.ThreadID)
	}
	want := []string{"start", "resume", "turn", "collect"}
	if got := runner.calls; len(got) != len(want) {
		t.Fatalf("calls = %v, want %v", got, want)
	} else {
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("calls = %v, want %v", got, want)
			}
		}
	}
}
