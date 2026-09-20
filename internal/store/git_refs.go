package store

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
)

var ErrGitRefConflict = errors.New("git ref conflict")

type GitRef struct {
	ID          string
	TaskID      string
	AttemptID   string
	Branch      string
	CommitSHA   string
	RemoteState string
}

// RecordGitRef records a ref for an owned attempt. Ref records are append-only:
// repeating the same binding is idempotent, while changing it is a conflict.
func (s *Store) RecordGitRef(ctx context.Context, taskID, attemptID, branch, commitSHA, remoteState string) (GitRef, bool, error) {
	if len(commitSHA) != 40 {
		return GitRef{}, false, fmt.Errorf("commit SHA must be exactly 40 hexadecimal characters")
	}
	if _, err := hex.DecodeString(commitSHA); err != nil {
		return GitRef{}, false, fmt.Errorf("commit SHA must be exactly 40 hexadecimal characters: %w", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return GitRef{}, false, fmt.Errorf("begin git ref: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := getAttempt(ctx, tx, taskID, attemptID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return GitRef{}, false, ErrAttemptNotFound
		}
		return GitRef{}, false, fmt.Errorf("check git ref attempt: %w", err)
	}

	var existing GitRef
	err = tx.QueryRowContext(ctx, "SELECT id, task_id, attempt_id, branch, commit_sha, remote_state FROM git_refs WHERE task_id = ? AND attempt_id = ?", taskID, attemptID).Scan(&existing.ID, &existing.TaskID, &existing.AttemptID, &existing.Branch, &existing.CommitSHA, &existing.RemoteState)
	if err == nil {
		if existing.Branch != branch || existing.CommitSHA != commitSHA || existing.RemoteState != remoteState {
			return GitRef{}, false, ErrGitRefConflict
		}
		return existing, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return GitRef{}, false, fmt.Errorf("load git ref: %w", err)
	}

	ref := GitRef{ID: newStoreID("git-ref"), TaskID: taskID, AttemptID: attemptID, Branch: branch, CommitSHA: commitSHA, RemoteState: remoteState}
	if _, err := tx.ExecContext(ctx, "INSERT INTO git_refs(id, task_id, attempt_id, branch, commit_sha, remote_state) VALUES (?, ?, ?, ?, ?, ?)", ref.ID, ref.TaskID, ref.AttemptID, ref.Branch, ref.CommitSHA, ref.RemoteState); err != nil {
		return GitRef{}, false, fmt.Errorf("insert git ref: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return GitRef{}, false, fmt.Errorf("commit git ref: %w", err)
	}
	return ref, true, nil
}
