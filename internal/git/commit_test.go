package git

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeRunner struct {
	calls []string
	fn    func(context.Context, string, string, ...string) (string, error)
}

func (f *fakeRunner) Run(ctx context.Context, dir, name string, args ...string) (string, error) {
	f.calls = append(f.calls, name+" "+strings.Join(args, " "))
	return f.fn(ctx, dir, name, args...)
}

func TestCommitValidatesThenStagesAndCommits(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "repo")
	runner := &fakeRunner{fn: func(_ context.Context, _ string, name string, args ...string) (string, error) {
		if name != "git" {
			return "", nil
		}
		if len(args) >= 2 && args[0] == "rev-parse" && args[1] == "--show-toplevel" {
			return workspace + "\n", nil
		}
		if len(args) >= 2 && args[0] == "rev-parse" && args[1] == "HEAD" {
			return "0123456789012345678901234567890123456789\n", nil
		}
		return "", nil
	}}

	sha, err := Commit(context.Background(), workspace, []string{"go", "test", "./..."}, "test commit", runner)
	if err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	if sha != "0123456789012345678901234567890123456789" {
		t.Fatalf("sha = %q", sha)
	}
	if got, want := strings.Join(runner.calls, "\n"), "git rev-parse --show-toplevel\ngo test ./...\ngit add --all\ngit commit -m test commit\ngit rev-parse HEAD"; got != want {
		t.Fatalf("calls =\n%s\nwant\n%s", got, want)
	}
}

func TestCommitValidationFailureDoesNotCommit(t *testing.T) {
	runner := &fakeRunner{fn: func(_ context.Context, _ string, name string, args ...string) (string, error) {
		if name == "git" && args[0] == "rev-parse" {
			return `C:\repo` + "\n", nil
		}
		return "", errors.New("validation failed")
	}}
	_, err := Commit(context.Background(), `C:\repo`, []string{"check"}, "msg", runner)
	if err == nil {
		t.Fatal("expected validation error")
	}
	for _, call := range runner.calls {
		if strings.HasPrefix(call, "git add") || strings.HasPrefix(call, "git commit") {
			t.Fatalf("commit operation after validation failure: %s", call)
		}
	}
}

func TestCommitRejectsWorkspaceAndValidation(t *testing.T) {
	for name, workspace := range map[string]string{"empty workspace": "", "relative workspace": `relative/repo`} {
		t.Run(name, func(t *testing.T) {
			_, err := Commit(context.Background(), workspace, []string{"check"}, "msg", &fakeRunner{})
			if err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
	_, err := Commit(context.Background(), filepath.Join(t.TempDir(), "repo"), nil, "msg", &fakeRunner{})
	if err == nil {
		t.Fatal("expected empty validation rejection")
	}
}

func TestCommitRejectsWorkspaceMismatchAndBadSHA(t *testing.T) {
	runner := &fakeRunner{fn: func(_ context.Context, _ string, name string, args ...string) (string, error) {
		if name == "git" && args[1] == "--show-toplevel" {
			return `C:\other` + "\n", nil
		}
		return "", nil
	}}
	_, err := Commit(context.Background(), `C:\repo`, []string{"check"}, "msg", runner)
	if err == nil {
		t.Fatal("expected workspace mismatch")
	}

	workspace := filepath.Join(t.TempDir(), "repo")
	runner = &fakeRunner{fn: func(_ context.Context, _ string, name string, args ...string) (string, error) {
		if name == "git" && args[1] == "--show-toplevel" {
			return workspace, nil
		}
		if name == "git" && args[1] == "HEAD" {
			return "not-a-sha", nil
		}
		return "", nil
	}}
	_, err = Commit(context.Background(), workspace, []string{"check"}, "msg", runner)
	if err == nil {
		t.Fatal("expected invalid SHA")
	}
}

func TestCommitValidationTimeoutAndCommandFailure(t *testing.T) {
	runner := &fakeRunner{fn: func(ctx context.Context, _ string, name string, args ...string) (string, error) {
		if name == "git" && args[1] == "--show-toplevel" {
			return `C:\repo`, nil
		}
		<-ctx.Done()
		return "", ctx.Err()
	}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	_, err := Commit(ctx, `C:\repo`, []string{"check"}, "msg", runner)
	if err == nil {
		t.Fatal("expected timeout")
	}

	runner = &fakeRunner{fn: func(_ context.Context, _ string, _ string, _ ...string) (string, error) { return "", errors.New("command failed") }}
	_, err = Commit(context.Background(), `C:\repo`, []string{"check"}, "msg", runner)
	if err == nil {
		t.Fatal("expected command failure")
	}
}

func TestCommitNeverRunsRemoteOrPush(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "repo")
	runner := &fakeRunner{fn: func(_ context.Context, _ string, name string, args ...string) (string, error) {
		if name == "git" && args[1] == "--show-toplevel" {
			return workspace, nil
		}
		if name == "git" && args[1] == "HEAD" {
			return "0123456789012345678901234567890123456789", nil
		}
		return "", nil
	}}
	if _, err := Commit(context.Background(), workspace, []string{"check"}, "msg", runner); err != nil {
		t.Fatal(err)
	}
	for _, call := range runner.calls {
		if strings.Contains(call, "remote") || strings.Contains(call, "push") {
			t.Fatalf("forbidden operation: %s", call)
		}
	}
}
