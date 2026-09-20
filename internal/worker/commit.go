package worker

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/WilliamLi0623/codex-homelab/internal/git"
)

var ErrCommitConfiguration = errors.New("worker commit configuration is incomplete or invalid")

// CommitConfiguredWorkspace validates and commits an attempt workspace only
// when the worker receives an explicit JSON command array. It never publishes
// to a remote repository.
func CommitConfiguredWorkspace(ctx context.Context, environment map[string]string, runner git.Runner) (string, error) {
	workspace := strings.TrimSpace(environment["CODEX_WORKSPACE"])
	rawCommand := strings.TrimSpace(environment["CODEX_VALIDATION_COMMAND"])
	if workspace == "" && rawCommand == "" {
		return "", nil
	}
	if workspace == "" || rawCommand == "" {
		return "", ErrCommitConfiguration
	}
	var command []string
	if err := json.Unmarshal([]byte(rawCommand), &command); err != nil || len(command) == 0 || strings.TrimSpace(command[0]) == "" {
		return "", ErrCommitConfiguration
	}
	for _, part := range command {
		if strings.TrimSpace(part) == "" {
			return "", ErrCommitConfiguration
		}
	}
	return git.Commit(ctx, workspace, command, "codex: complete attempt", runner)
}
