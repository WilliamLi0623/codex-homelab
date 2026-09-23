package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/WilliamLi0623/codex-homelab/internal/git"
)

type commandRunner struct{}

// Runner executes a command with separated arguments in dir.
type Runner = git.Runner

func (commandRunner) Run(ctx context.Context, dir, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), err
	}
	return string(out), nil
}

// Prepare creates an attempt-scoped local checkout at workspace.
// It never runs remote or push operations and refuses to reuse any path.
func Prepare(ctx context.Context, repository, baseRef, workspace, attemptID string, runner Runner) error {
	if ctx == nil {
		return errors.New("invalid workspace request")
	}
	if strings.TrimSpace(repository) == "" || strings.TrimSpace(baseRef) == "" || !safeAttemptID(attemptID) {
		return errors.New("invalid workspace request")
	}
	if workspace == "" || !filepath.IsAbs(workspace) || filepath.Base(workspace) != attemptID {
		return errors.New("invalid workspace request")
	}

	info, err := os.Stat(workspace)
	if err == nil {
		if info.IsDir() {
			return errors.New("workspace already exists")
		}
		return errors.New("workspace path already exists")
	}
	if !errors.Is(err, os.ErrNotExist) {
		return errors.New("workspace cannot be inspected")
	}
	if err := os.MkdirAll(workspace, 0700); err != nil {
		return errors.New("workspace cannot be created")
	}
	if runner == nil {
		runner = commandRunner{}
	}

	if output, err := runner.Run(ctx, filepath.Dir(workspace), "git", "clone", "--no-checkout", "--branch", baseRef, "--", cloneSource(repository), workspace); err != nil {
		return fmt.Errorf("workspace clone failed: %w%s", err, diagnosticSuffix(output))
	}
	if output, err := runner.Run(ctx, workspace, "git", "-C", workspace, "checkout", "--detach", baseRef); err != nil {
		return fmt.Errorf("workspace checkout failed: %w%s", err, diagnosticSuffix(output))
	}
	return nil
}

func cloneSource(repository string) string {
	repository = strings.TrimSpace(repository)
	parts := strings.Split(repository, "/")
	if len(parts) == 2 && validRepositoryPart(parts[0]) && validRepositoryPart(parts[1]) {
		return "https://github.com/" + parts[0] + "/" + parts[1] + ".git"
	}
	return repository
}

func validRepositoryPart(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range value {
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') &&
			(char < '0' || char > '9') && char != '-' && char != '_' && char != '.' {
			return false
		}
	}
	return true
}

func diagnosticSuffix(output string) string {
	output = strings.TrimSpace(output)
	if output == "" || strings.Contains(strings.ToLower(output), "token") || strings.Contains(strings.ToLower(output), "password") || strings.Contains(strings.ToLower(output), "secret") {
		return ""
	}
	if len(output) > 512 {
		output = output[:512]
	}
	return ": " + output
}

func safeAttemptID(value string) bool {
	if value == "" || len(value) > 64 || value == "." || value == ".." {
		return false
	}
	for _, char := range value {
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') &&
			(char < '0' || char > '9') && char != '-' && char != '_' && char != '.' {
			return false
		}
	}
	return true
}
