package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

var (
	ErrExecutionHandleNotFound = errors.New("execution handle not found")
	ErrExecutionHandleConflict = errors.New("execution handle conflict")
)

type ExecutionHandle struct {
	ID         string
	AttemptID  string
	Executor   string
	ExternalID string
	State      string
	CreatedAt  time.Time
}

func (s *Store) RecordExecutionHandle(ctx context.Context, attemptID, executor, externalID, state string) (ExecutionHandle, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ExecutionHandle{}, false, fmt.Errorf("begin execution handle: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var exists int
	if err := tx.QueryRowContext(ctx, "SELECT 1 FROM task_attempts WHERE id = ?", attemptID).Scan(&exists); err == sql.ErrNoRows {
		return ExecutionHandle{}, false, ErrAttemptNotFound
	} else if err != nil {
		return ExecutionHandle{}, false, fmt.Errorf("check attempt for execution handle: %w", err)
	}
	var existing ExecutionHandle
	err = tx.QueryRowContext(ctx, "SELECT id, attempt_id, executor, external_id, state FROM execution_handles WHERE attempt_id = ? ORDER BY id LIMIT 1", attemptID).Scan(&existing.ID, &existing.AttemptID, &existing.Executor, &existing.ExternalID, &existing.State)
	if err == nil {
		if existing.Executor != executor || existing.ExternalID != externalID {
			return ExecutionHandle{}, false, ErrExecutionHandleConflict
		}
		return existing, false, nil
	}
	if err != sql.ErrNoRows {
		return ExecutionHandle{}, false, fmt.Errorf("load execution handle: %w", err)
	}
	now := time.Now().UTC()
	handle := ExecutionHandle{ID: newStoreID("execution"), AttemptID: attemptID, Executor: executor, ExternalID: externalID, State: state, CreatedAt: now}
	if _, err := tx.ExecContext(ctx, "INSERT INTO execution_handles(id,attempt_id,executor,external_id,state) VALUES (?,?,?,?,?)", handle.ID, handle.AttemptID, handle.Executor, handle.ExternalID, handle.State); err != nil {
		return ExecutionHandle{}, false, fmt.Errorf("insert execution handle: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return ExecutionHandle{}, false, fmt.Errorf("commit execution handle: %w", err)
	}
	return handle, true, nil
}

func (s *Store) GetExecutionHandle(ctx context.Context, attemptID string) (ExecutionHandle, error) {
	return s.getExecutionHandle(ctx, "WHERE attempt_id = ?", attemptID)
}

func (s *Store) GetExecutionHandleByExternalID(ctx context.Context, externalID string) (ExecutionHandle, error) {
	return s.getExecutionHandle(ctx, "WHERE external_id = ?", externalID)
}

func (s *Store) getExecutionHandle(ctx context.Context, clause, value string) (ExecutionHandle, error) {
	var handle ExecutionHandle
	err := s.db.QueryRowContext(ctx, "SELECT id, attempt_id, executor, external_id, state FROM execution_handles "+clause+" ORDER BY id LIMIT 1", value).Scan(&handle.ID, &handle.AttemptID, &handle.Executor, &handle.ExternalID, &handle.State)
	if err == sql.ErrNoRows {
		return ExecutionHandle{}, ErrExecutionHandleNotFound
	}
	if err != nil {
		return ExecutionHandle{}, fmt.Errorf("load execution handle: %w", err)
	}
	return handle, nil
}

func (s *Store) UpdateExecutionHandleState(ctx context.Context, externalID, state string) error {
	result, err := s.db.ExecContext(ctx, "UPDATE execution_handles SET state = ? WHERE external_id = ?", state, externalID)
	if err != nil {
		return fmt.Errorf("update execution handle: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("count execution handle update: %w", err)
	}
	if count == 0 {
		return ErrExecutionHandleNotFound
	}
	return nil
}
