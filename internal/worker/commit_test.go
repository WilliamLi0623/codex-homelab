package worker

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/WilliamLi0623/codex-homelab/internal/git"
)

type fakeRunner struct {
	calls int
	fn    func(context.Context, string, string, ...string) (string, error)
}

func (r *fakeRunner) Run(ctx context.Context, dir, name string, args ...string) (string, error) {
	r.calls++
	return r.fn(ctx, dir, name, args...)
}

var _ git.Runner = (*fakeRunner)(nil)

func TestCommitConfiguredWorkspaceIsOptionalOrRequiresCompleteConfig(t *testing.T) {
	if sha, err := CommitConfiguredWorkspace(context.Background(), map[string]string{}, nil); err != nil || sha != "" {
		t.Fatalf("unconfigured = %q, %v", sha, err)
	}
	if _, err := CommitConfiguredWorkspace(context.Background(), map[string]string{"CODEX_WORKSPACE": "/workspace/a"}, nil); !errors.Is(err, ErrCommitConfiguration) {
		t.Fatalf("partial config error = %v", err)
	}
	if _, err := CommitConfiguredWorkspace(context.Background(), map[string]string{"CODEX_WORKSPACE": "/workspace/a", "CODEX_VALIDATION_COMMAND": "not-json"}, nil); !errors.Is(err, ErrCommitConfiguration) {
		t.Fatalf("invalid command error = %v", err)
	}
}

func TestCommitConfiguredWorkspaceUsesSeparatedValidationAndNoPush(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "attempt-1")
	runner := &fakeRunner{fn: func(_ context.Context, _ string, name string, args ...string) (string, error) {
		if name == "git" && len(args) >= 2 && args[0] == "rev-parse" && args[1] == "--show-toplevel" {
			return workspace + "\n", nil
		}
		if name == "git" && len(args) >= 2 && args[0] == "rev-parse" && args[1] == "HEAD" {
			return "0123456789012345678901234567890123456789\n", nil
		}
		return "", nil
	}}
	sha, err := CommitConfiguredWorkspace(context.Background(), map[string]string{
		"CODEX_WORKSPACE":          workspace,
		"CODEX_VALIDATION_COMMAND": `["go","test","./..."]`,
	}, runner)
	if err != nil || sha != "0123456789012345678901234567890123456789" {
		t.Fatalf("commit = %q, %v", sha, err)
	}
	if runner.calls != 5 {
		t.Fatalf("runner calls = %d, want validation plus four git operations", runner.calls)
	}
}
