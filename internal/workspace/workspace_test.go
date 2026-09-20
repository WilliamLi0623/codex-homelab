package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type recordingRunner struct {
	calls []commandCall
	errAt int
}

type commandCall struct {
	dir  string
	name string
	args []string
}

func (r *recordingRunner) Run(_ context.Context, dir, name string, args ...string) (string, error) {
	r.calls = append(r.calls, commandCall{dir: dir, name: name, args: append([]string(nil), args...)})
	if len(r.calls) == r.errAt {
		return "remote-token=secret", errors.New("runner failed")
	}
	return "", nil
}

func TestPrepareClonesAndDetachesAttemptWorkspace(t *testing.T) {
	root := t.TempDir()
	attemptID := "attempt-123"
	workspace := filepath.Join(root, attemptID)
	runner := &recordingRunner{}

	if err := Prepare(context.Background(), "https://example.invalid/repo.git", "refs/heads/main", workspace, attemptID, runner); err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if got, want := len(runner.calls), 2; got != want {
		t.Fatalf("calls = %d, want %d", got, want)
	}
	clone := runner.calls[0]
	if clone.name != "git" || !sameArgs(clone.args, "clone", "--no-checkout", "--branch", "refs/heads/main", "--", "https://example.invalid/repo.git", workspace) {
		t.Fatalf("clone call = %#v", clone)
	}
	detach := runner.calls[1]
	if detach.dir != workspace || detach.name != "git" || !sameArgs(detach.args, "-C", workspace, "checkout", "--detach", "refs/heads/main") {
		t.Fatalf("detach call = %#v", detach)
	}
}

func TestPrepareRejectsInvalidOrNonIsolatedWorkspace(t *testing.T) {
	root := t.TempDir()
	valid := filepath.Join(root, "attempt-123")
	cases := []struct{ name, repository, baseRef, workspace, attemptID string }{
		{"empty repository", "", "main", valid, "attempt-123"},
		{"empty base ref", "repo", "", valid, "attempt-123"},
		{"empty workspace", "repo", "main", "", "attempt-123"},
		{"empty attempt", "repo", "main", valid, ""},
		{"unsafe attempt", "repo", "main", valid, "attempt/123"},
		{"relative workspace", "repo", "main", filepath.Join("relative", "attempt-123"), "attempt-123"},
		{"basename mismatch", "repo", "main", filepath.Join(root, "other"), "attempt-123"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runner := &recordingRunner{}
			if err := Prepare(context.Background(), tc.repository, tc.baseRef, tc.workspace, tc.attemptID, runner); err == nil {
				t.Fatal("expected validation error")
			}
			if len(runner.calls) != 0 {
				t.Fatalf("runner calls = %d, want 0", len(runner.calls))
			}
		})
	}

	if err := os.MkdirAll(valid, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(valid, "existing"), []byte("data"), 0600); err != nil {
		t.Fatal(err)
	}
	runner := &recordingRunner{}
	if err := Prepare(context.Background(), "repo", "main", valid, "attempt-123", runner); err == nil {
		t.Fatal("expected non-empty workspace rejection")
	}
	if len(runner.calls) != 0 {
		t.Fatalf("runner calls = %d, want 0", len(runner.calls))
	}
}

func TestPrepareFailsClosedOnExistingEmptyWorkspace(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "attempt-123")
	if err := os.MkdirAll(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	runner := &recordingRunner{}
	if err := Prepare(context.Background(), "repo", "main", workspace, "attempt-123", runner); err == nil {
		t.Fatal("expected existing workspace rejection")
	}
	if len(runner.calls) != 0 {
		t.Fatalf("runner calls = %d, want 0", len(runner.calls))
	}
}

func TestPrepareDoesNotPushAndSanitizesRunnerErrors(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "attempt-123")
	runner := &recordingRunner{errAt: 1}
	err := Prepare(context.Background(), "https://user:secret@example.invalid/repo.git", "main", workspace, "attempt-123", runner)
	if err == nil || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "remote-token") || strings.Contains(err.Error(), "git clone") {
		t.Fatalf("error = %v, want sanitized failure", err)
	}
	for _, call := range runner.calls {
		if call.name == "git" && (contains(call.args, "push") || contains(call.args, "remote")) {
			t.Fatalf("forbidden operation: %#v", call)
		}
	}
}

func sameArgs(got []string, want ...string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func contains(args []string, value string) bool {
	for _, arg := range args {
		if arg == value {
			return true
		}
	}
	return false
}
