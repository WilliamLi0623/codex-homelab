package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrExecutionSpecConflict = errors.New("attempt execution spec conflicts with existing spec")
	ErrExecutionSpecInvalid  = errors.New("attempt execution spec is invalid")
	ErrExecutionSpecNotFound = errors.New("attempt execution spec not found")
)

type AttemptExecutionSpec struct {
	ID                string
	TaskID            string
	AttemptID         string
	Branch            string
	ValidationCommand []string
	CreatedAt         string
	UpdatedAt         string
}

func (s *Store) EnsureAttemptExecutionSpec(ctx context.Context, spec AttemptExecutionSpec) (AttemptExecutionSpec, bool, error) {
	if err := validateExecutionSpec(spec); err != nil {
		return AttemptExecutionSpec{}, false, err
	}
	commandJSON, err := json.Marshal(spec.ValidationCommand)
	if err != nil {
		return AttemptExecutionSpec{}, false, ErrExecutionSpecInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AttemptExecutionSpec{}, false, fmt.Errorf("begin execution spec: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var attemptExists int
	err = tx.QueryRowContext(ctx, "SELECT 1 FROM task_attempts WHERE task_id = ? AND id = ?", spec.TaskID, spec.AttemptID).Scan(&attemptExists)
	if errors.Is(err, sql.ErrNoRows) {
		return AttemptExecutionSpec{}, false, ErrAttemptNotFound
	}
	if err != nil {
		return AttemptExecutionSpec{}, false, fmt.Errorf("check execution attempt: %w", err)
	}
	var existing AttemptExecutionSpec
	var existingJSON string
	err = tx.QueryRowContext(ctx, `SELECT id, task_id, attempt_id, branch, validation_command_json, created_at, updated_at FROM attempt_execution_specs WHERE task_id = ? AND attempt_id = ?`, spec.TaskID, spec.AttemptID).
		Scan(&existing.ID, &existing.TaskID, &existing.AttemptID, &existing.Branch, &existingJSON, &existing.CreatedAt, &existing.UpdatedAt)
	if err == nil {
		if err := json.Unmarshal([]byte(existingJSON), &existing.ValidationCommand); err != nil {
			return AttemptExecutionSpec{}, false, ErrExecutionSpecInvalid
		}
		if existing.Branch != spec.Branch || !sameStrings(existing.ValidationCommand, spec.ValidationCommand) {
			return AttemptExecutionSpec{}, false, ErrExecutionSpecConflict
		}
		return existing, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return AttemptExecutionSpec{}, false, fmt.Errorf("read execution spec: %w", err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if spec.ID == "" {
		spec.ID = newStoreID("execution-spec")
	}
	if spec.CreatedAt == "" {
		spec.CreatedAt = now
	}
	spec.UpdatedAt = now
	if _, err := tx.ExecContext(ctx, `INSERT INTO attempt_execution_specs(id, task_id, attempt_id, branch, validation_command_json, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`, spec.ID, spec.TaskID, spec.AttemptID, spec.Branch, string(commandJSON), spec.CreatedAt, spec.UpdatedAt); err != nil {
		return AttemptExecutionSpec{}, false, fmt.Errorf("insert execution spec: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return AttemptExecutionSpec{}, false, fmt.Errorf("commit execution spec: %w", err)
	}
	return spec, true, nil
}

func (s *Store) GetAttemptExecutionSpec(ctx context.Context, taskID, attemptID string) (AttemptExecutionSpec, error) {
	var spec AttemptExecutionSpec
	var commandJSON string
	err := s.db.QueryRowContext(ctx, `SELECT id, task_id, attempt_id, branch, validation_command_json, created_at, updated_at FROM attempt_execution_specs WHERE task_id = ? AND attempt_id = ?`, taskID, attemptID).
		Scan(&spec.ID, &spec.TaskID, &spec.AttemptID, &spec.Branch, &commandJSON, &spec.CreatedAt, &spec.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return AttemptExecutionSpec{}, ErrExecutionSpecNotFound
	}
	if err != nil {
		return AttemptExecutionSpec{}, fmt.Errorf("read execution spec: %w", err)
	}
	if err := json.Unmarshal([]byte(commandJSON), &spec.ValidationCommand); err != nil {
		return AttemptExecutionSpec{}, ErrExecutionSpecInvalid
	}
	if err := validateExecutionSpec(spec); err != nil {
		return AttemptExecutionSpec{}, err
	}
	return spec, nil
}

// ListPendingAttemptExecutionSpecs returns dispatches that do not yet have a
// durable completion record. The completion ID is deterministic, so a
// restarted observer can avoid recollecting an already-consumed worker result.
func (s *Store) ListPendingAttemptExecutionSpecs(ctx context.Context) ([]AttemptExecutionSpec, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT s.id, s.task_id, s.attempt_id, s.branch, s.validation_command_json, s.created_at, s.updated_at
FROM attempt_execution_specs s
JOIN task_attempts a ON a.task_id = s.task_id AND a.id = s.attempt_id
WHERE a.state NOT IN ('CANCELLED', 'PROVIDER_FAILED', 'EXECUTION_FAILED', 'VALIDATION_FAILED', 'COMPLETED', 'UNKNOWN')
AND (NOT EXISTS (SELECT 1 FROM validation_results v WHERE v.id = 'completion-' || s.attempt_id)
   OR EXISTS (
       SELECT 1 FROM release_progress rp
       WHERE rp.task_id = s.task_id
         AND rp.attempt_id = s.attempt_id
         AND NOT (rp.step = 'DONE' AND rp.state = 'COMPLETED')
   ))
ORDER BY s.created_at, s.id`)
	if err != nil {
		return nil, fmt.Errorf("list pending execution specs: %w", err)
	}
	defer rows.Close()
	var specs []AttemptExecutionSpec
	for rows.Next() {
		var spec AttemptExecutionSpec
		var commandJSON string
		if err := rows.Scan(&spec.ID, &spec.TaskID, &spec.AttemptID, &spec.Branch, &commandJSON, &spec.CreatedAt, &spec.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan pending execution spec: %w", err)
		}
		if err := json.Unmarshal([]byte(commandJSON), &spec.ValidationCommand); err != nil {
			return nil, ErrExecutionSpecInvalid
		}
		if err := validateExecutionSpec(spec); err != nil {
			return nil, err
		}
		specs = append(specs, spec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate pending execution specs: %w", err)
	}
	return specs, nil
}

func validateExecutionSpec(spec AttemptExecutionSpec) error {
	if spec.TaskID == "" || spec.AttemptID == "" || strings.TrimSpace(spec.Branch) == "" || len(spec.ValidationCommand) == 0 || len(spec.ValidationCommand) > 64 {
		return ErrExecutionSpecInvalid
	}
	for _, part := range spec.ValidationCommand {
		if strings.TrimSpace(part) == "" || len(part) > 4096 {
			return ErrExecutionSpecInvalid
		}
	}
	return nil
}

func sameStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
