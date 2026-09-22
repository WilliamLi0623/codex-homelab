package store

import (
	"context"
	"fmt"

	"github.com/WilliamLi0623/codex-homelab/internal/domain"
)

// ListAttempts returns the durable attempts for a task in creation order.
// A missing task is distinguished from a task that has no attempts yet.
func (s *Store) ListAttempts(ctx context.Context, taskID string) ([]domain.Attempt, error) {
	if _, err := s.GetTask(ctx, taskID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, "SELECT id, task_id, attempt_number, model_profile, state FROM task_attempts WHERE task_id = ? ORDER BY attempt_number ASC", taskID)
	if err != nil {
		return nil, fmt.Errorf("list attempts: %w", err)
	}
	defer rows.Close()
	var attempts []domain.Attempt
	for rows.Next() {
		var attempt domain.Attempt
		var state string
		if err := rows.Scan(&attempt.ID, &attempt.TaskID, &attempt.Number, &attempt.ModelProfile, &state); err != nil {
			return nil, fmt.Errorf("scan attempt: %w", err)
		}
		attempt.State = domain.AttemptState(state)
		attempts = append(attempts, attempt)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate attempts: %w", err)
	}
	return attempts, nil
}
