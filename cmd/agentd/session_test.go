package main

import (
	"bufio"
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/WilliamLi0623/codex-homelab/internal/agentd"
	"github.com/WilliamLi0623/codex-homelab/internal/workspace"
)

func TestDecodeNextRequestAllowsCumulativeInputOverOneMiB(t *testing.T) {
	line := `{"prompt":"` + strings.Repeat("x", 700_000) + `"}` + "\n"
	input := strings.Repeat(line, 2)
	reader := bufio.NewReader(strings.NewReader(input))
	for i := 0; i < 2; i++ {
		if _, err := decodeNextRequest(reader); err != nil {
			t.Fatalf("request %d: %v", i+1, err)
		}
	}
}

func TestDecodeNextRequestRejectsSingleOversizedJSONLine(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader(`{"prompt":"` + strings.Repeat("x", 1<<20) + `"}` + "\n"))
	if _, err := decodeNextRequest(reader); err == nil {
		t.Fatal("decodeNextRequest() accepted an oversized request")
	}
}

func TestValidateEnvironmentRequiresAttemptSpecificCodexHome(t *testing.T) {
	cases := []struct {
		name string
		env  []string
	}{
		{name: "relative codex home", env: []string{"CODEX_HOME=attempt-1", "CODEX_ATTEMPT_ID=attempt-1"}},
		{name: "missing codex home", env: []string{"CODEX_ATTEMPT_ID=attempt-1"}},
		{name: "empty codex home", env: []string{"CODEX_HOME=", "CODEX_ATTEMPT_ID=attempt-1"}},
		{name: "missing attempt id", env: []string{"CODEX_HOME=/tmp/attempt-1"}},
		{name: "empty attempt id", env: []string{"CODEX_HOME=/tmp/attempt-1", "CODEX_ATTEMPT_ID="}},
		{name: "mismatched final directory", env: []string{"CODEX_HOME=/tmp/other", "CODEX_ATTEMPT_ID=attempt-1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateEnvironment(tc.env); err == nil {
				t.Fatalf("validateEnvironment(%v) accepted invalid environment", tc.env)
			}
		})
	}
}

func TestValidateEnvironmentAcceptsSafeAttemptSpecificCodexHome(t *testing.T) {
	env := []string{fmt.Sprintf("CODEX_HOME=/tmp/%s", "attempt-1"), "CODEX_ATTEMPT_ID=attempt-1"}
	if err := validateEnvironment(env); err != nil {
		t.Fatalf("validateEnvironment() error = %v", err)
	}
}

type workspaceRunner struct {
	calls []struct {
		dir  string
		name string
		args []string
	}
}

func (r *workspaceRunner) Run(_ context.Context, dir, name string, args ...string) (string, error) {
	r.calls = append(r.calls, struct {
		dir  string
		name string
		args []string
	}{dir: dir, name: name, args: append([]string(nil), args...)})
	return "", nil
}

var _ workspace.Runner = (*workspaceRunner)(nil)

func TestPrepareConfiguredWorkspaceIsOptionalOrComplete(t *testing.T) {
	base := []string{"CODEX_HOME=/tmp/attempt-1", "CODEX_ATTEMPT_ID=attempt-1"}
	if err := prepareConfiguredWorkspace(context.Background(), base, &workspaceRunner{}); err != nil {
		t.Fatalf("unconfigured workspace error = %v", err)
	}
	if err := prepareConfiguredWorkspace(context.Background(), append(base, "CODEX_REPOSITORY=repo"), &workspaceRunner{}); err == nil {
		t.Fatal("partial workspace configuration accepted")
	}

	root := t.TempDir()
	runner := &workspaceRunner{}
	env := append(base,
		"CODEX_REPOSITORY=https://example.invalid/repo.git",
		"CODEX_BASE_REF=refs/heads/main",
		"CODEX_WORKSPACE="+filepath.Join(root, "attempt-1"),
	)
	if err := prepareConfiguredWorkspace(context.Background(), env, runner); err != nil {
		t.Fatalf("complete workspace configuration error = %v", err)
	}
	if len(runner.calls) != 2 {
		t.Fatalf("workspace calls = %d, want 2", len(runner.calls))
	}
}

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
