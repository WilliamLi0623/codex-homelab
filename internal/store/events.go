package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"time"
)

type TaskMessage struct {
	ID        string
	TaskID    string
	Role      string
	Body      string
	CreatedAt time.Time
}

type TaskEvent struct {
	ID        string
	TaskID    string
	Type      string
	Payload   string
	CreatedAt time.Time
}

func (s *Store) AppendUserMessage(ctx context.Context, taskID, body string) (TaskMessage, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return TaskMessage{}, fmt.Errorf("begin message append: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var exists int
	err = tx.QueryRowContext(ctx, "SELECT 1 FROM tasks WHERE id = ?", taskID).Scan(&exists)
	if err == sql.ErrNoRows {
		return TaskMessage{}, ErrTaskNotFound
	}
	if err != nil {
		return TaskMessage{}, fmt.Errorf("query task for message: %w", err)
	}

	now := time.Now().UTC()
	message := TaskMessage{ID: newStoreID("message"), TaskID: taskID, Role: "user", Body: body, CreatedAt: now}
	if _, err := tx.ExecContext(ctx, "INSERT INTO task_messages(id, task_id, role, body, created_at) VALUES (?, ?, ?, ?, ?)", message.ID, message.TaskID, message.Role, message.Body, message.CreatedAt.Format(time.RFC3339Nano)); err != nil {
		return TaskMessage{}, fmt.Errorf("insert task message: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO task_events(id, task_id, event_type, payload_json, created_at) VALUES (?, ?, ?, ?, ?)", newStoreID("event"), taskID, "task.message_received", "{}", now.Format(time.RFC3339Nano)); err != nil {
		return TaskMessage{}, fmt.Errorf("insert message event: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return TaskMessage{}, fmt.Errorf("commit message append: %w", err)
	}
	return message, nil
}

func (s *Store) ListTaskEvents(ctx context.Context, taskID string) ([]TaskEvent, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id, task_id, event_type, payload_json, created_at FROM task_events WHERE task_id = ? ORDER BY created_at, id", taskID)
	if err != nil {
		return nil, fmt.Errorf("list task events: %w", err)
	}
	defer rows.Close()

	var events []TaskEvent
	for rows.Next() {
		var event TaskEvent
		var createdAt string
		if err := rows.Scan(&event.ID, &event.TaskID, &event.Type, &event.Payload, &createdAt); err != nil {
			return nil, fmt.Errorf("scan task event: %w", err)
		}
		parsed, err := time.Parse(time.RFC3339Nano, createdAt)
		if err != nil {
			return nil, fmt.Errorf("parse task event timestamp: %w", err)
		}
		event.CreatedAt = parsed
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate task events: %w", err)
	}
	return events, nil
}

func newStoreID(prefix string) string {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	return prefix + "-" + hex.EncodeToString(bytes)
}
