package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

var (
	ErrContinuationConflict        = errors.New("task continuation idempotency conflict")
	ErrContinuationNotFound        = errors.New("task continuation not found")
	ErrContinuationDeliveryUnknown = errors.New("task continuation delivery outcome is unknown")
)

const (
	ContinuationPending   = "PENDING"
	ContinuationDelivered = "DELIVERED"
	ContinuationRejected  = "REJECTED"
	ContinuationUnknown   = "UNKNOWN"
)

type TaskContinuation struct {
	ID             string
	TaskID         string
	AttemptID      string
	IdempotencyKey string
	Body           string
	State          string
	ErrorSummary   string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// StartContinuation atomically records the user message and its delivery
// intent. A duplicate idempotency key returns the original intent without
// appending another message.
func (s *Store) StartContinuation(ctx context.Context, taskID, attemptID, idempotencyKey, body string) (TaskContinuation, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return TaskContinuation{}, false, fmt.Errorf("begin continuation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := getTask(ctx, tx, taskID); errors.Is(err, sql.ErrNoRows) {
		return TaskContinuation{}, false, ErrTaskNotFound
	} else if err != nil {
		return TaskContinuation{}, false, err
	}
	if _, err := getAttempt(ctx, tx, taskID, attemptID); errors.Is(err, sql.ErrNoRows) {
		return TaskContinuation{}, false, ErrAttemptNotFound
	} else if err != nil {
		return TaskContinuation{}, false, err
	}

	var existing TaskContinuation
	var createdAt, updatedAt string
	err = tx.QueryRowContext(ctx, `SELECT id, task_id, attempt_id, idempotency_key, body, state, error_summary, created_at, updated_at
		FROM task_continuations WHERE task_id = ? AND attempt_id = ? AND idempotency_key = ?`, taskID, attemptID, idempotencyKey).Scan(
		&existing.ID, &existing.TaskID, &existing.AttemptID, &existing.IdempotencyKey, &existing.Body, &existing.State, &existing.ErrorSummary, &createdAt, &updatedAt)
	if err == nil {
		existing.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
		if err == nil {
			existing.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt)
		}
		if err != nil {
			return TaskContinuation{}, false, fmt.Errorf("parse continuation timestamps: %w", err)
		}
		if existing.Body != body {
			return TaskContinuation{}, false, ErrContinuationConflict
		}
		return existing, false, nil
	}
	if err != sql.ErrNoRows {
		return TaskContinuation{}, false, fmt.Errorf("load continuation idempotency record: %w", err)
	}

	now := time.Now().UTC()
	continuation := TaskContinuation{
		ID:             newStoreID("continuation"),
		TaskID:         taskID,
		AttemptID:      attemptID,
		IdempotencyKey: idempotencyKey,
		Body:           body,
		State:          ContinuationPending,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO task_continuations(id, task_id, attempt_id, idempotency_key, body, state, error_summary, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, '', ?, ?)`, continuation.ID, continuation.TaskID, continuation.AttemptID, continuation.IdempotencyKey, continuation.Body, continuation.State, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		return TaskContinuation{}, false, fmt.Errorf("insert continuation: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO task_messages(id, task_id, attempt_id, role, body, created_at) VALUES (?, ?, ?, 'user', ?, ?)`, newStoreID("message"), taskID, attemptID, body, now.Format(time.RFC3339Nano)); err != nil {
		return TaskContinuation{}, false, fmt.Errorf("insert continuation message: %w", err)
	}
	payload := fmt.Sprintf(`{"continuation_id":"%s"}`, continuation.ID)
	if _, err := tx.ExecContext(ctx, `INSERT INTO task_events(id, task_id, attempt_id, event_type, payload_json, created_at) VALUES (?, ?, ?, 'task.continuation_requested', ?, ?)`, newStoreID("event"), taskID, attemptID, payload, now.Format(time.RFC3339Nano)); err != nil {
		return TaskContinuation{}, false, fmt.Errorf("record continuation event: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return TaskContinuation{}, false, fmt.Errorf("commit continuation: %w", err)
	}
	return continuation, true, nil
}

func (s *Store) GetContinuation(ctx context.Context, id string) (TaskContinuation, error) {
	var continuation TaskContinuation
	var createdAt, updatedAt string
	err := s.db.QueryRowContext(ctx, `SELECT id, task_id, attempt_id, idempotency_key, body, state, error_summary, created_at, updated_at
		FROM task_continuations WHERE id = ?`, id).Scan(&continuation.ID, &continuation.TaskID, &continuation.AttemptID, &continuation.IdempotencyKey, &continuation.Body, &continuation.State, &continuation.ErrorSummary, &createdAt, &updatedAt)
	if err == sql.ErrNoRows {
		return TaskContinuation{}, ErrContinuationNotFound
	}
	if err != nil {
		return TaskContinuation{}, fmt.Errorf("load continuation: %w", err)
	}
	continuation.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err == nil {
		continuation.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt)
	}
	if err != nil {
		return TaskContinuation{}, fmt.Errorf("parse continuation timestamps: %w", err)
	}
	return continuation, nil
}

func (s *Store) UpdateContinuationState(ctx context.Context, id, state, errorSummary string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE task_continuations SET state = ?, error_summary = ?, updated_at = ? WHERE id = ?`, state, errorSummary, time.Now().UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		return fmt.Errorf("update continuation: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("count continuation update: %w", err)
	}
	if count == 0 {
		return ErrContinuationNotFound
	}
	return nil
}
