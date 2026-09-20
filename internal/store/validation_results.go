package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

var ErrValidationResultConflict = errors.New("validation result idempotency conflict")
var ErrValidationResultNotFound = errors.New("validation result not found")

type ValidationResult struct {
	ID        string
	AttemptID string
	Command   string
	State     string
	OutputRef string
	CreatedAt time.Time
}

// RecordValidationResult persists metadata and a reference to validation output.
// rawOutput is intentionally ignored: raw command output must live outside the
// event stream and is never accepted as event payload.
func (s *Store) RecordValidationResult(ctx context.Context, result ValidationResult, rawOutput string) (ValidationResult, error) {
	if result.ID == "" || result.AttemptID == "" || result.Command == "" || result.State == "" || result.CreatedAt.IsZero() {
		return ValidationResult{}, fmt.Errorf("validation result requires id, attempt_id, command, state, and created_at")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ValidationResult{}, fmt.Errorf("begin validation result: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var exists int
	if err := tx.QueryRowContext(ctx, "SELECT 1 FROM task_attempts WHERE id = ?", result.AttemptID).Scan(&exists); err == sql.ErrNoRows {
		return ValidationResult{}, ErrAttemptNotFound
	} else if err != nil {
		return ValidationResult{}, fmt.Errorf("check validation attempt: %w", err)
	}

	var existing ValidationResult
	var createdAt string
	var outputRef sql.NullString
	err = tx.QueryRowContext(ctx, "SELECT id, attempt_id, command, state, output_ref, created_at FROM validation_results WHERE id = ?", result.ID).Scan(&existing.ID, &existing.AttemptID, &existing.Command, &existing.State, &outputRef, &createdAt)
	if err == nil {
		existing.OutputRef = outputRef.String
		existing.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
		if err != nil {
			return ValidationResult{}, fmt.Errorf("parse existing validation timestamp: %w", err)
		}
		if existing == result {
			if err := tx.Commit(); err != nil {
				return ValidationResult{}, fmt.Errorf("commit validation replay: %w", err)
			}
			return existing, nil
		}
		return ValidationResult{}, ErrValidationResultConflict
	}
	if err != sql.ErrNoRows {
		return ValidationResult{}, fmt.Errorf("check existing validation result: %w", err)
	}

	if _, err := tx.ExecContext(ctx, "INSERT INTO validation_results(id, attempt_id, command, state, output_ref, created_at) VALUES (?, ?, ?, ?, ?, ?)", result.ID, result.AttemptID, result.Command, result.State, nullableString(result.OutputRef), result.CreatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return ValidationResult{}, fmt.Errorf("insert validation result: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return ValidationResult{}, fmt.Errorf("commit validation result: %w", err)
	}
	return result, nil
}

func (s *Store) GetValidationResult(ctx context.Context, id string) (ValidationResult, error) {
	var result ValidationResult
	var createdAt string
	var outputRef sql.NullString
	err := s.db.QueryRowContext(ctx, "SELECT id, attempt_id, command, state, output_ref, created_at FROM validation_results WHERE id = ?", id).Scan(&result.ID, &result.AttemptID, &result.Command, &result.State, &outputRef, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ValidationResult{}, ErrValidationResultNotFound
	}
	if err != nil {
		return ValidationResult{}, err
	}
	result.OutputRef = outputRef.String
	result.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return ValidationResult{}, fmt.Errorf("parse validation timestamp: %w", err)
	}
	return result, nil
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}
