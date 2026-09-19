package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/WilliamLi0623/codex-homelab/internal/domain"
)

var ErrIdempotencyConflict = errors.New("idempotency key belongs to a different request")

var ErrTaskNotFound = errors.New("task not found")

func (s *Store) CreateTask(ctx context.Context, task domain.Task) (domain.Task, bool, error) {
	requestHash := taskRequestHash(task)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Task{}, false, fmt.Errorf("begin task creation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var existingID, existingHash string
	err = tx.QueryRowContext(ctx, "SELECT task_id, request_hash FROM task_intake WHERE idempotency_key = ?", task.IdempotencyKey).Scan(&existingID, &existingHash)
	if err == nil {
		if existingHash != requestHash {
			return domain.Task{}, false, ErrIdempotencyConflict
		}
		existing, err := getTask(ctx, tx, existingID)
		if err != nil {
			return domain.Task{}, false, err
		}
		return existing, false, nil
	}
	if err != sql.ErrNoRows {
		return domain.Task{}, false, fmt.Errorf("query idempotency key: %w", err)
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO repositories(id, remote_url, created_at) VALUES (?, ?, ?)", task.Repository, task.Repository, now); err != nil {
		return domain.Task{}, false, fmt.Errorf("create repository: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO tasks(id, repository_id, base_ref, objective, execution_class, state, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)", task.ID, task.Repository, task.BaseRef, task.Objective, task.ExecutionClass, task.State, now); err != nil {
		return domain.Task{}, false, fmt.Errorf("create task: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO task_intake(idempotency_key, task_id, request_hash, created_at) VALUES (?, ?, ?, ?)", task.IdempotencyKey, task.ID, requestHash, now); err != nil {
		return domain.Task{}, false, fmt.Errorf("record task intake: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO task_events(id, task_id, event_type, payload_json, created_at) VALUES (?, ?, ?, ?, ?)", task.ID+":received", task.ID, "task.received", "{}", now); err != nil {
		return domain.Task{}, false, fmt.Errorf("record task event: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return domain.Task{}, false, fmt.Errorf("commit task creation: %w", err)
	}
	return task, true, nil
}

func (s *Store) GetTask(ctx context.Context, id string) (domain.Task, error) {
	task, err := getTask(ctx, s.db, id)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Task{}, ErrTaskNotFound
	}
	return task, err
}

func (s *Store) ListTasks(ctx context.Context) ([]domain.Task, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id, repository_id, base_ref, objective, execution_class, state FROM tasks ORDER BY created_at, id")
	if err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}
	defer rows.Close()

	var tasks []domain.Task
	for rows.Next() {
		var task domain.Task
		var executionClass, state string
		if err := rows.Scan(&task.ID, &task.Repository, &task.BaseRef, &task.Objective, &executionClass, &state); err != nil {
			return nil, fmt.Errorf("scan task: %w", err)
		}
		task.ExecutionClass = domain.ExecutionClass(executionClass)
		task.State = domain.TaskState(state)
		tasks = append(tasks, task)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate tasks: %w", err)
	}
	return tasks, nil
}

func (s *Store) CancelTask(ctx context.Context, id string) (domain.Task, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Task{}, fmt.Errorf("begin task cancellation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	task, err := getTask(ctx, tx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Task{}, ErrTaskNotFound
	}
	if err != nil {
		return domain.Task{}, err
	}
	if task.State == domain.TaskCancelled {
		return task, nil
	}
	if err := task.TransitionTo(domain.TaskCancelled); err != nil {
		return domain.Task{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, "UPDATE tasks SET state = ? WHERE id = ?", task.State, task.ID); err != nil {
		return domain.Task{}, fmt.Errorf("persist task cancellation: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO task_events(id, task_id, event_type, payload_json, created_at) VALUES (?, ?, ?, ?, ?)", newStoreID("event"), task.ID, "task.cancelled", "{}", now); err != nil {
		return domain.Task{}, fmt.Errorf("record task cancellation: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return domain.Task{}, fmt.Errorf("commit task cancellation: %w", err)
	}
	return task, nil
}

func getTask(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id string) (domain.Task, error) {
	var task domain.Task
	var executionClass string
	var state string
	err := queryer.QueryRowContext(ctx, "SELECT id, repository_id, base_ref, objective, execution_class, state FROM tasks WHERE id = ?", id).Scan(&task.ID, &task.Repository, &task.BaseRef, &task.Objective, &executionClass, &state)
	if err != nil {
		return domain.Task{}, fmt.Errorf("load task %q: %w", id, err)
	}
	task.ExecutionClass = domain.ExecutionClass(executionClass)
	task.State = domain.TaskState(state)
	return task, nil
}

func taskRequestHash(task domain.Task) string {
	payload := task.Repository + "\x00" + task.BaseRef + "\x00" + task.Objective + "\x00" + string(task.ExecutionClass)
	digest := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(digest[:])
}
