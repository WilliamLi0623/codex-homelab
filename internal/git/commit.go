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
	baseSHA, err := run(runner, ctx, workspace, "git", "rev-parse", "HEAD")
	if err != nil {
		return "", fmt.Errorf("read base commit SHA: %w", err)
	}
	baseSHA = strings.TrimSpace(baseSHA)
	if !sha40.MatchString(baseSHA) {
		return "", fmt.Errorf("invalid base commit SHA %q", baseSHA)
	}
	output, err := run(runner, ctx, workspace, validation[0], validation[1:]...)
	if err != nil {
		return "", fmt.Errorf("validation failed: command=%s: %w; output=%q", strings.Join(validation, " "), err, summarizeCommandOutput(output))
	}
	if output, err := run(runner, ctx, workspace, "git", "add", "--all"); err != nil {
		return "", fmt.Errorf("git add failed: %w; output=%q", err, summarizeCommandOutput(output))
	}
	if output, err := run(runner, ctx, workspace, "git", "commit", "-m", message); err != nil {
		if isNothingToCommit(output) {
			currentSHA, readErr := run(runner, ctx, workspace, "git", "rev-parse", "HEAD")
			currentSHA = strings.TrimSpace(currentSHA)
			if readErr == nil && currentSHA != baseSHA && sha40.MatchString(currentSHA) {
				return currentSHA, nil
			}
		}
		return "", fmt.Errorf("git commit failed: %w; output=%q", err, summarizeCommandOutput(output))
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

func isNothingToCommit(output string) bool {
	output = strings.ToLower(output)
	return strings.Contains(output, "nothing to commit") || strings.Contains(output, "nothing added to commit")
}

const maxCommandOutput = 4096

func summarizeCommandOutput(output string) string {
	output = strings.TrimSpace(output)
	if len(output) > maxCommandOutput {
		return output[:maxCommandOutput] + "..."
	}
	return output
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
