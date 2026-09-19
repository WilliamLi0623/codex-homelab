package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

var ErrCodexThreadConflict = errors.New("attempt already maps to a different Codex thread")
var ErrCodexThreadNotFound = errors.New("codex thread not found")

type CodexThread struct {
	ID            string
	AttemptID     string
	CodexThreadID string
	CreatedAt     time.Time
}

// RecordCodexThread durably binds one Controller attempt to one App Server
// thread. Repeating the same mapping is safe; replacing it is forbidden.
func (s *Store) RecordCodexThread(ctx context.Context, attemptID, codexThreadID string) (CodexThread, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return CodexThread{}, false, fmt.Errorf("begin codex thread record: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var exists int
	err = tx.QueryRowContext(ctx, "SELECT 1 FROM task_attempts WHERE id = ?", attemptID).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return CodexThread{}, false, ErrAttemptNotFound
	}
	if err != nil {
		return CodexThread{}, false, fmt.Errorf("check attempt for codex thread: %w", err)
	}

	existing, err := getCodexThread(ctx, tx, attemptID)
	if err == nil {
		if existing.CodexThreadID != codexThreadID {
			return CodexThread{}, false, ErrCodexThreadConflict
		}
		return existing, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return CodexThread{}, false, err
	}

	thread := CodexThread{ID: newStoreID("codex-thread"), AttemptID: attemptID, CodexThreadID: codexThreadID, CreatedAt: time.Now().UTC()}
	if _, err := tx.ExecContext(ctx, "INSERT INTO codex_threads(id, attempt_id, codex_thread_id, created_at) VALUES (?, ?, ?, ?)", thread.ID, thread.AttemptID, thread.CodexThreadID, thread.CreatedAt.Format(time.RFC3339Nano)); err != nil {
		return CodexThread{}, false, fmt.Errorf("insert codex thread: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return CodexThread{}, false, fmt.Errorf("commit codex thread: %w", err)
	}
	return thread, true, nil
}

func (s *Store) GetCodexThread(ctx context.Context, attemptID string) (CodexThread, error) {
	thread, err := getCodexThread(ctx, s.db, attemptID)
	if errors.Is(err, sql.ErrNoRows) {
		return CodexThread{}, ErrCodexThreadNotFound
	}
	return thread, err
}

func getCodexThread(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, attemptID string) (CodexThread, error) {
	var thread CodexThread
	var createdAt string
	err := queryer.QueryRowContext(ctx, "SELECT id, attempt_id, codex_thread_id, created_at FROM codex_threads WHERE attempt_id = ?", attemptID).Scan(&thread.ID, &thread.AttemptID, &thread.CodexThreadID, &createdAt)
	if err != nil {
		return CodexThread{}, err
	}
	parsed, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return CodexThread{}, fmt.Errorf("parse codex thread timestamp: %w", err)
	}
	thread.CreatedAt = parsed
	return thread, nil
}
