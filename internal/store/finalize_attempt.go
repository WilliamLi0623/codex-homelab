package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/WilliamLi0623/codex-homelab/internal/domain"
)

var ErrAttemptFinalizationRejected = errors.New("attempt success finalization rejected")

// FinalizeAttemptSuccess catches up the durable state machine only after the
// worker completion record has been persisted. Each skipped observable phase is
// still recorded as an event; UNKNOWN and other terminal outcomes are never
// promoted to success.
func (s *Store) FinalizeAttemptSuccess(ctx context.Context, taskID, attemptID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin attempt finalization: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	task, err := getTask(ctx, tx, taskID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrTaskNotFound
	}
	if err != nil {
		return err
	}
	attempt, err := getAttempt(ctx, tx, taskID, attemptID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrAttemptNotFound
	}
	if err != nil {
		return err
	}
	if attempt.State == domain.AttemptCompleted && task.State == domain.TaskSucceeded {
		return nil
	}
	if attempt.State == domain.AttemptUnknown || isTerminalAttemptStateExceptCompleted(attempt.State) || isTerminalTaskStateExceptSucceeded(task.State) {
		return fmt.Errorf("%w: task=%s attempt=%s", ErrAttemptFinalizationRejected, task.State, attempt.State)
	}

	attemptStates := []domain.AttemptState{domain.AttemptStarting, domain.AttemptRunning, domain.AttemptCompleted}
	for _, next := range attemptStates {
		if attempt.State == next {
			continue
		}
		if err := attempt.TransitionTo(next); err != nil {
			return fmt.Errorf("%w: %v", ErrAttemptFinalizationRejected, err)
		}
		if _, err := tx.ExecContext(ctx, "UPDATE task_attempts SET state = ? WHERE id = ?", attempt.State, attempt.ID); err != nil {
			return fmt.Errorf("persist attempt finalization: %w", err)
		}
		if err := insertLifecycleEvent(ctx, tx, task.ID, attempt.ID, "attempt."+string(next)); err != nil {
			return err
		}
	}

	taskStates := []domain.TaskState{domain.TaskPlanned, domain.TaskWaitingForCapacity, domain.TaskQueued, domain.TaskDispatched, domain.TaskRunning, domain.TaskValidating, domain.TaskPublishing, domain.TaskSucceeded}
	for _, next := range taskStates {
		if task.State == next {
			continue
		}
		if err := task.TransitionTo(next); err != nil {
			return fmt.Errorf("%w: %v", ErrAttemptFinalizationRejected, err)
		}
		if _, err := tx.ExecContext(ctx, "UPDATE tasks SET state = ? WHERE id = ?", task.State, task.ID); err != nil {
			return fmt.Errorf("persist task finalization: %w", err)
		}
		if err := insertLifecycleEvent(ctx, tx, task.ID, attempt.ID, "task."+string(next)); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit attempt finalization: %w", err)
	}
	return nil
}

func insertLifecycleEvent(ctx context.Context, tx *sql.Tx, taskID, attemptID, eventType string) error {
	if _, err := tx.ExecContext(ctx, "INSERT INTO task_events(id, task_id, attempt_id, event_type, payload_json, created_at) VALUES (?, ?, ?, ?, ?, ?)", newStoreID("event"), taskID, attemptID, eventType, "{}", time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("record lifecycle event: %w", err)
	}
	return nil
}

func isTerminalAttemptStateExceptCompleted(state domain.AttemptState) bool {
	return state == domain.AttemptProviderFailed || state == domain.AttemptExecutionFailed || state == domain.AttemptValidationFailed || state == domain.AttemptCancelled
}

func isTerminalTaskStateExceptSucceeded(state domain.TaskState) bool {
	return state == domain.TaskFailed || state == domain.TaskBlocked || state == domain.TaskCancelled
}
