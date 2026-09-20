package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/WilliamLi0623/codex-homelab/internal/executor/k3s"
	"github.com/WilliamLi0623/codex-homelab/internal/store"
)

var (
	ErrCompletionInput          = errors.New("completion input is invalid")
	ErrCompletionResultMissing  = errors.New("worker result has no commit SHA")
	ErrCompletionReleasePending = errors.New("completion is durable but capacity release is pending")
)

type ResultCollector interface {
	CollectResult(context.Context, string) (k3s.Result, error)
}

type CompletionInput struct {
	TaskID            string
	AttemptID         string
	Branch            string
	ValidationCommand []string
}

type ResultConsumer struct {
	store    *store.Store
	results  ResultCollector
	capacity Capacity
}

func NewResultConsumer(database *store.Store, results ResultCollector, capacity Capacity) *ResultConsumer {
	return &ResultConsumer{store: database, results: results, capacity: capacity}
}

// Complete collects one durable worker result, atomically records validation
// and the local git ref, then attempts capacity release. A release failure is
// returned after durable completion so reconciliation can retry cleanup without
// rerunning the worker or commit.
func (c *ResultConsumer) Complete(ctx context.Context, input CompletionInput) (store.CompletionRecord, error) {
	if c == nil || c.store == nil || c.results == nil || c.capacity == nil || input.TaskID == "" || input.AttemptID == "" || input.Branch == "" || len(input.ValidationCommand) == 0 {
		return store.CompletionRecord{}, ErrCompletionInput
	}
	for _, part := range input.ValidationCommand {
		if strings.TrimSpace(part) == "" {
			return store.CompletionRecord{}, ErrCompletionInput
		}
	}
	command := strings.Join(input.ValidationCommand, " ")
	completionID := "completion-" + input.AttemptID

	claim, err := c.store.GetCapacityClaim(ctx, input.TaskID, input.AttemptID)
	if err != nil {
		return store.CompletionRecord{}, fmt.Errorf("load capacity claim: %w", err)
	}
	completion, err := c.loadExistingCompletion(ctx, input, completionID, command)
	if errors.Is(err, store.ErrValidationResultConflict) || errors.Is(err, store.ErrGitRefNotFound) {
		return store.CompletionRecord{}, fmt.Errorf("load completion: %w", err)
	}
	if err != nil && !errors.Is(err, store.ErrValidationResultNotFound) {
		return store.CompletionRecord{}, err
	}
	if errors.Is(err, store.ErrValidationResultNotFound) {
		handle, handleErr := c.store.GetExecutionHandle(ctx, input.AttemptID)
		if handleErr != nil {
			return store.CompletionRecord{}, fmt.Errorf("load execution handle: %w", handleErr)
		}
		if handle.State == string(k3s.HandleUnknown) {
			return store.CompletionRecord{}, k3s.ErrUnknownUnresolved
		}
		result, collectErr := c.results.CollectResult(ctx, handle.ExternalID)
		if collectErr != nil {
			return store.CompletionRecord{}, collectErr
		}
		if result.CommitSHA == "" {
			return store.CompletionRecord{}, ErrCompletionResultMissing
		}
		completion, err = c.store.RecordAttemptCompletion(ctx, store.CompletionRecord{ID: completionID, TaskID: input.TaskID, AttemptID: input.AttemptID, Command: command, ValidationState: "PASSED", Branch: input.Branch, CommitSHA: result.CommitSHA})
		if err != nil {
			return store.CompletionRecord{}, fmt.Errorf("record completion: %w", err)
		}
	}
	if err := c.capacity.Release(ctx, Claim{ID: claim.ID, VMID: claim.VMID}); err != nil {
		return completion, fmt.Errorf("%w: %v", ErrCompletionReleasePending, err)
	}
	return completion, nil
}

func (c *ResultConsumer) loadExistingCompletion(ctx context.Context, input CompletionInput, id, command string) (store.CompletionRecord, error) {
	validation, err := c.store.GetValidationResult(ctx, id)
	if errors.Is(err, store.ErrValidationResultNotFound) {
		return store.CompletionRecord{}, err
	}
	if err != nil {
		return store.CompletionRecord{}, err
	}
	ref, err := c.store.GetGitRef(ctx, input.TaskID, input.AttemptID)
	if err != nil {
		return store.CompletionRecord{}, err
	}
	if validation.AttemptID != input.AttemptID || validation.Command != command || validation.State != "PASSED" {
		return store.CompletionRecord{}, store.ErrCompletionConflict
	}
	return store.CompletionRecord{ID: id, TaskID: input.TaskID, AttemptID: input.AttemptID, Command: command, ValidationState: validation.State, Branch: ref.Branch, CommitSHA: ref.CommitSHA}, nil
}
