package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/WilliamLi0623/codex-homelab/internal/domain"
)

// StartAttempt creates the one initial append-only Attempt for a newly received Task.
// Retries use RetryTask and therefore cannot accidentally create a second initial Attempt.
func (s *Store) StartAttempt(ctx context.Context, taskID, modelProfile string) (domain.Task, domain.Attempt, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Task{}, domain.Attempt{}, fmt.Errorf("begin initial attempt: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	task, err := getTask(ctx, tx, taskID)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Task{}, domain.Attempt{}, ErrTaskNotFound
	}
	if err != nil {
		return domain.Task{}, domain.Attempt{}, err
	}
	var existing string
	err = tx.QueryRowContext(ctx, "SELECT id FROM task_attempts WHERE task_id = ? LIMIT 1", taskID).Scan(&existing)
	if err == nil {
		return domain.Task{}, domain.Attempt{}, ErrAttemptStillActive
	}
	if err != sql.ErrNoRows {
		return domain.Task{}, domain.Attempt{}, fmt.Errorf("check initial attempt: %w", err)
	}
	if err := task.TransitionTo(domain.TaskPlanned); err != nil {
		return domain.Task{}, domain.Attempt{}, err
	}
	attempt := domain.NewAttempt(newStoreID("attempt"), taskID, 1, modelProfile)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, "UPDATE tasks SET state = ? WHERE id = ?", task.State, taskID); err != nil {
		return domain.Task{}, domain.Attempt{}, fmt.Errorf("persist initial task state: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO task_attempts(id,task_id,attempt_number,model_profile,state,created_at) VALUES (?,?,?,?,?,?)", attempt.ID, attempt.TaskID, attempt.Number, attempt.ModelProfile, attempt.State, now); err != nil {
		return domain.Task{}, domain.Attempt{}, fmt.Errorf("insert initial attempt: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO task_events(id,task_id,attempt_id,event_type,payload_json,created_at) VALUES (?,?,?,?,?,?)", newStoreID("event"), taskID, attempt.ID, "attempt.created", "{}", now); err != nil {
		return domain.Task{}, domain.Attempt{}, fmt.Errorf("record initial attempt: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return domain.Task{}, domain.Attempt{}, fmt.Errorf("commit initial attempt: %w", err)
	}
	return task, attempt, nil
}
