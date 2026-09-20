package store

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

var ErrCompletionConflict = errors.New("attempt completion conflicts with an existing completion")

// CompletionRecord contains only durable validation and repository metadata.
// Worker output is intentionally not part of this record.
type CompletionRecord struct {
	ID              string
	TaskID          string
	AttemptID       string
	Command         string
	ValidationState string
	Branch          string
	CommitSHA       string
}

// RecordAttemptCompletion atomically records validation, the local git ref,
// and a safe completion summary. It does not change task or attempt state.
func (s *Store) RecordAttemptCompletion(ctx context.Context, record CompletionRecord) (CompletionRecord, error) {
	if err := validateCompletion(record); err != nil {
		return CompletionRecord{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return CompletionRecord{}, fmt.Errorf("begin attempt completion: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := getAttempt(ctx, tx, record.TaskID, record.AttemptID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return CompletionRecord{}, ErrAttemptNotFound
		}
		return CompletionRecord{}, fmt.Errorf("check completion ownership: %w", err)
	}

	var existing CompletionRecord
	err = tx.QueryRowContext(ctx, "SELECT id, attempt_id, command, state FROM validation_results WHERE id = ?", record.ID).
		Scan(&existing.ID, &existing.AttemptID, &existing.Command, &existing.ValidationState)
	if err == nil {
		if err := loadCompletionGitRef(ctx, tx, &existing, record.TaskID); err != nil {
			return CompletionRecord{}, err
		}
		if existing == record {
			return record, nil
		}
		return CompletionRecord{}, ErrCompletionConflict
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return CompletionRecord{}, fmt.Errorf("check existing completion: %w", err)
	}

	if _, err := tx.ExecContext(ctx, "INSERT INTO validation_results(id, attempt_id, command, state, output_ref, created_at) VALUES (?, ?, ?, ?, NULL, ?)", record.ID, record.AttemptID, record.Command, record.ValidationState, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return CompletionRecord{}, fmt.Errorf("insert completion validation: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO git_refs(id, task_id, attempt_id, branch, commit_sha, remote_state) VALUES (?, ?, ?, ?, ?, ?)", newStoreID("git-ref"), record.TaskID, record.AttemptID, record.Branch, record.CommitSHA, "LOCAL"); err != nil {
		return CompletionRecord{}, fmt.Errorf("insert completion git ref: %w", err)
	}
	payload, err := json.Marshal(map[string]string{"summary": "validation passed and local commit recorded", "state": record.ValidationState, "branch": record.Branch, "commit_sha": record.CommitSHA})
	if err != nil {
		return CompletionRecord{}, fmt.Errorf("encode completion event: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO task_events(id, task_id, attempt_id, event_type, payload_json, created_at) VALUES (?, ?, ?, ?, ?, ?)", newStoreID("event"), record.TaskID, record.AttemptID, "attempt.completed", string(payload), time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return CompletionRecord{}, fmt.Errorf("insert completion event: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return CompletionRecord{}, fmt.Errorf("commit attempt completion: %w", err)
	}
	return record, nil
}

func validateCompletion(record CompletionRecord) error {
	if record.ID == "" || record.TaskID == "" || record.AttemptID == "" || record.Command == "" || record.Branch == "" || record.CommitSHA == "" {
		return errors.New("completion requires id, task, attempt, command, branch, and commit SHA")
	}
	if record.ValidationState != "PASSED" {
		return errors.New("completion validation state must be PASSED")
	}
	if len(record.CommitSHA) != 40 {
		return errors.New("commit SHA must be exactly 40 hexadecimal characters")
	}
	if _, err := hex.DecodeString(record.CommitSHA); err != nil {
		return errors.New("commit SHA must be exactly 40 hexadecimal characters")
	}
	return nil
}

func loadCompletionGitRef(ctx context.Context, tx *sql.Tx, record *CompletionRecord, taskID string) error {
	var remoteState string
	err := tx.QueryRowContext(ctx, "SELECT task_id, attempt_id, branch, commit_sha, remote_state FROM git_refs WHERE task_id = ? AND attempt_id = ?", taskID, record.AttemptID).
		Scan(&record.TaskID, &record.AttemptID, &record.Branch, &record.CommitSHA, &remoteState)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrCompletionConflict
	}
	if err != nil {
		return fmt.Errorf("load existing completion git ref: %w", err)
	}
	return nil
}
