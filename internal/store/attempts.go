package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/WilliamLi0623/codex-homelab/internal/domain"
)

var ErrAttemptNotFound = errors.New("attempt not found")
var ErrAttemptRequiresReconciliation = errors.New("attempt outcome requires reconciliation")
var ErrAttemptStillActive = errors.New("attempt is still active")

// ReconcileUnknownAttempt persists a verified terminal result for an attempt
// that was previously UNKNOWN. It intentionally cannot resume an attempt.
func (s *Store) ReconcileUnknownAttempt(ctx context.Context, taskID, attemptID string, outcome domain.AttemptState) (domain.Attempt, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Attempt{}, fmt.Errorf("begin attempt reconciliation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	attempt, err := getAttempt(ctx, tx, taskID, attemptID)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Attempt{}, ErrAttemptNotFound
	}
	if err != nil {
		return domain.Attempt{}, err
	}
	if err := attempt.ReconcileTo(outcome); err != nil {
		return domain.Attempt{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, "UPDATE task_attempts SET state = ? WHERE id = ?", attempt.State, attempt.ID); err != nil {
		return domain.Attempt{}, fmt.Errorf("persist attempt reconciliation: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO task_events(id, task_id, attempt_id, event_type, payload_json, created_at) VALUES (?, ?, ?, ?, ?, ?)", newStoreID("event"), taskID, attempt.ID, "attempt.reconciled", `{"outcome":"`+string(attempt.State)+`"}`, now); err != nil {
		return domain.Attempt{}, fmt.Errorf("record attempt reconciliation: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return domain.Attempt{}, fmt.Errorf("commit attempt reconciliation: %w", err)
	}
	return attempt, nil
}

func getAttempt(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, taskID, attemptID string) (domain.Attempt, error) {
	var attempt domain.Attempt
	var state string
	err := queryer.QueryRowContext(ctx, "SELECT id, task_id, attempt_number, model_profile, state FROM task_attempts WHERE task_id = ? AND id = ?", taskID, attemptID).Scan(&attempt.ID, &attempt.TaskID, &attempt.Number, &attempt.ModelProfile, &state)
	if err != nil {
		return domain.Attempt{}, err
	}
	attempt.State = domain.AttemptState(state)
	return attempt, nil
}
