package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/WilliamLi0623/codex-homelab/internal/capacity"
	"github.com/WilliamLi0623/codex-homelab/internal/executor/k3s"
	"github.com/WilliamLi0623/codex-homelab/internal/store"
)

var (
	ErrCompletionInput          = errors.New("completion input is invalid")
	ErrCompletionResultMissing  = errors.New("worker result has no commit SHA")
	ErrCompletionReleasePending = errors.New("completion is durable but capacity release is pending")
	ErrCompletionNotReady       = errors.New("worker execution is not complete")
	ErrWorkerFailed             = errors.New("worker execution failed")
	ErrWorkerCancelled          = errors.New("worker execution was cancelled")
	ErrObservationUnavailable   = errors.New("worker observation is unavailable")
)

type ResultCollector interface {
	CollectResult(context.Context, string) (k3s.Result, error)
}

type executionCanceller interface {
	Cancel(context.Context, string) error
}

type ResultObserver interface {
	Observe(context.Context, string) (k3s.Job, error)
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
	observer ResultObserver
}

func NewResultConsumer(database *store.Store, results ResultCollector, capacity Capacity) *ResultConsumer {
	consumer := &ResultConsumer{store: database, results: results, capacity: capacity}
	if observer, ok := results.(ResultObserver); ok {
		consumer.observer = observer
	}
	return consumer
}

// ObserveAndComplete is the background-observer boundary. A worker keeps its
// HTTP result endpoint alive after the initial turn, so a complete result can
// legitimately become available while the Kubernetes Job is still Running.
// In that case the durable result protocol and commit SHA, rather than the Job
// phase alone, authorize Complete.
func (c *ResultConsumer) ObserveAndComplete(ctx context.Context, input CompletionInput) (k3s.Job, store.CompletionRecord, error) {
	if c == nil || c.observer == nil || input.AttemptID == "" {
		return k3s.Job{}, store.CompletionRecord{}, ErrObservationUnavailable
	}
	job, err := c.observer.Observe(ctx, input.AttemptID)
	if err != nil {
		return k3s.Job{}, store.CompletionRecord{}, err
	}
	switch job.State {
	case k3s.JobPending:
		return job, store.CompletionRecord{}, ErrCompletionNotReady
	case k3s.JobRunning:
		completion, err := c.Complete(ctx, input)
		if errors.Is(err, ErrCompletionResultMissing) || errors.Is(err, k3s.ErrWorkerNotReady) {
			return job, store.CompletionRecord{}, ErrCompletionNotReady
		}
		return job, completion, err
	case k3s.JobFailed:
		return job, store.CompletionRecord{}, ErrWorkerFailed
	case k3s.JobCancelled:
		return job, store.CompletionRecord{}, ErrWorkerCancelled
	case k3s.JobSucceeded:
		completion, err := c.Complete(ctx, input)
		return job, completion, err
	default:
		return job, store.CompletionRecord{}, k3s.ErrUnknown
	}
}

// ObserveAndCompleteStored reconstructs completion input after a controller
// restart. It deliberately refuses to guess the branch or validation command.
func (c *ResultConsumer) ObserveAndCompleteStored(ctx context.Context, taskID, attemptID string) (k3s.Job, store.CompletionRecord, error) {
	if c == nil || c.store == nil || taskID == "" || attemptID == "" {
		return k3s.Job{}, store.CompletionRecord{}, ErrObservationUnavailable
	}
	spec, err := c.store.GetAttemptExecutionSpec(ctx, taskID, attemptID)
	if err != nil {
		return k3s.Job{}, store.CompletionRecord{}, err
	}
	input := CompletionInput{TaskID: taskID, AttemptID: attemptID, Branch: spec.Branch, ValidationCommand: spec.ValidationCommand}
	completion, completionErr := c.loadExistingCompletion(ctx, input, "completion-"+attemptID, strings.Join(spec.ValidationCommand, " "))
	if completionErr == nil {
		if err := c.releaseCompletedAttempt(ctx, input); err != nil {
			return k3s.Job{}, completion, err
		}
		return k3s.Job{ID: attemptID, AttemptID: attemptID, State: k3s.JobSucceeded}, completion, nil
	}
	if !errors.Is(completionErr, store.ErrValidationResultNotFound) {
		return k3s.Job{}, store.CompletionRecord{}, completionErr
	}
	return c.ObserveAndComplete(ctx, input)
}

// releaseCompletedAttempt resumes only the cleanup half of a durable
// completion. It must not observe or recollect the worker result: the worker
// Job may already have been removed while release was in an UNKNOWN state.
func (c *ResultConsumer) releaseCompletedAttempt(ctx context.Context, input CompletionInput) error {
	claim, err := c.store.GetCapacityClaim(ctx, input.TaskID, input.AttemptID)
	if err != nil {
		return fmt.Errorf("load capacity claim for completed release: %w", err)
	}
	kubeNode, err := capacity.DynamicHostname(claim.VMID, claim.Generation)
	if err != nil {
		return fmt.Errorf("derive worker identity for completed release: %w", err)
	}
	if err := c.capacity.Release(ctx, Claim{ID: claim.ID, VMID: claim.VMID, TaskID: claim.TaskID, AttemptID: claim.AttemptID, Generation: claim.Generation, KubeNode: kubeNode}); err != nil {
		return fmt.Errorf("%w: %v", ErrCompletionReleasePending, err)
	}
	return nil
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
	var handle store.ExecutionHandle
	handleLoaded := false
	if errors.Is(err, store.ErrValidationResultNotFound) {
		var handleErr error
		handle, handleErr = c.store.GetExecutionHandle(ctx, input.AttemptID)
		if handleErr != nil {
			return store.CompletionRecord{}, fmt.Errorf("load execution handle: %w", handleErr)
		}
		handleLoaded = true
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
	if !handleLoaded {
		var handleErr error
		handle, handleErr = c.store.GetExecutionHandle(ctx, input.AttemptID)
		if handleErr != nil {
			return completion, fmt.Errorf("load execution handle: %w", handleErr)
		}
		if handle.State == string(k3s.HandleUnknown) {
			return completion, k3s.ErrUnknownUnresolved
		}
	}
	if canceller, ok := c.results.(executionCanceller); ok {
		if err := canceller.Cancel(ctx, handle.ExternalID); err != nil {
			return completion, fmt.Errorf("%w: cancel execution: %v", ErrCompletionReleasePending, err)
		}
	}
	if err := c.store.FinalizeAttemptSuccess(ctx, input.TaskID, input.AttemptID); err != nil {
		return completion, fmt.Errorf("finalize task and attempt state: %w", err)
	}
	kubeNode, identityErr := capacity.DynamicHostname(claim.VMID, claim.Generation)
	if identityErr != nil {
		return completion, fmt.Errorf("derive worker identity: %w", identityErr)
	}
	if err := c.capacity.Release(ctx, Claim{ID: claim.ID, VMID: claim.VMID, TaskID: claim.TaskID, AttemptID: claim.AttemptID, Generation: claim.Generation, KubeNode: kubeNode}); err != nil {
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
