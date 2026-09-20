package git

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// Runner executes a command in dir. Implementations may use ctx to enforce
// cancellation and timeouts.
type Runner interface {
	Run(ctx context.Context, dir, name string, args ...string) (string, error)
}

type commandRunner struct{}

func (commandRunner) Run(ctx context.Context, dir, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), err
	}
	return string(out), nil
}

var sha40 = regexp.MustCompile(`^[0-9a-fA-F]{40}$`)

// Commit runs validation first and creates one local commit only after it
// succeeds. It never contacts or modifies a remote repository.
func Commit(ctx context.Context, workspace string, validation []string, message string, runner Runner) (string, error) {
	if ctx == nil {
		return "", errors.New("nil context")
	}
	if workspace == "" || !filepath.IsAbs(workspace) {
		return "", errors.New("workspace must be a non-empty absolute path")
	}
	if len(validation) == 0 || strings.TrimSpace(validation[0]) == "" {
		return "", errors.New("validation command must not be empty")
	}
	if runner == nil {
		runner = commandRunner{}
	}

	top, err := run(runner, ctx, workspace, "git", "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("locate git workspace: %w", err)
	}
	if strings.TrimRight(top, "\r\n") != workspace {
		return "", fmt.Errorf("git workspace mismatch: got %q, want %q", strings.TrimSpace(top), workspace)
	}
	if _, err := run(runner, ctx, workspace, validation[0], validation[1:]...); err != nil {
		return "", fmt.Errorf("validation failed: %w", err)
	}
	if _, err := run(runner, ctx, workspace, "git", "add", "--all"); err != nil {
		return "", fmt.Errorf("git add failed: %w", err)
	}
	if _, err := run(runner, ctx, workspace, "git", "commit", "-m", message); err != nil {
		return "", fmt.Errorf("git commit failed: %w", err)
	}
	sha, err := run(runner, ctx, workspace, "git", "rev-parse", "HEAD")
	if err != nil {
		return "", fmt.Errorf("read commit SHA: %w", err)
	}
	sha = strings.TrimSpace(sha)
	if !sha40.MatchString(sha) {
		return "", fmt.Errorf("invalid commit SHA %q", sha)
	}
	return sha, nil
}

func run(runner Runner, ctx context.Context, dir, name string, args ...string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	out, err := runner.Run(ctx, dir, name, args...)
	if err != nil {
		return out, err
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	return out, nil
}
