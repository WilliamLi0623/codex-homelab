package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

const (
	ReleaseStepNone = "NONE"
	ReleaseStepCordon = "CORDON"
	ReleaseStepDrain = "DRAIN"
	ReleaseStepRemoveNode = "REMOVE_NODE"
	ReleaseStepVerifyNodeRemoved = "VERIFY_NODE_REMOVED"
	ReleaseStepVerifyIdentity = "VERIFY_IDENTITY"
	ReleaseStepStop = "STOP"
	ReleaseStepVerifyStopped = "VERIFY_STOPPED"
	ReleaseStepDestroy = "DESTROY"
	ReleaseStepDone = "DONE"
	ReleaseStatePending = "PENDING"
	ReleaseStateRunning = "RUNNING"
	ReleaseStateUnknown = "UNKNOWN"
	ReleaseStateCompleted = "COMPLETED"
)

var (
	ErrReleaseProgressConflict = errors.New("release progress conflicts with existing identity")
	ErrReleaseProgressInvalid = errors.New("release progress is invalid")
	ErrReleaseProgressNotFound = errors.New("release progress not found")
	ErrReleaseProgressRegress = errors.New("release progress cannot move backwards")
)

type ReleaseProgressRequest struct { TaskID, AttemptID string; VMID int; Generation, KubeNode string }
type ReleaseProgress struct {
	ID, TaskID, AttemptID string
	VMID int
	Generation, KubeNode, Step, State, ErrorSummary, UpdatedAt string
}

var releaseStepOrder = map[string]int{
	ReleaseStepNone: 0, ReleaseStepCordon: 1, ReleaseStepDrain: 2,
	ReleaseStepRemoveNode: 3, ReleaseStepVerifyNodeRemoved: 4,
	ReleaseStepVerifyIdentity: 5, ReleaseStepStop: 6,
	ReleaseStepVerifyStopped: 7, ReleaseStepDestroy: 8, ReleaseStepDone: 9,
}

func (s *Store) EnsureReleaseProgress(ctx context.Context, request ReleaseProgressRequest) (ReleaseProgress, bool, error) {
	if err := validateReleaseProgressRequest(request); err != nil { return ReleaseProgress{}, false, err }
	tx, err := s.db.BeginTx(ctx, nil); if err != nil { return ReleaseProgress{}, false, fmt.Errorf("begin release progress: %w", err) }
	defer func() { _ = tx.Rollback() }()
	existing, err := scanReleaseProgress(tx.QueryRowContext(ctx, `SELECT id, task_id, attempt_id, vmid, generation, kube_node, step, state, error_summary, updated_at FROM release_progress WHERE task_id = ? AND attempt_id = ?`, request.TaskID, request.AttemptID))
	if err == nil {
		if existing.VMID != request.VMID || existing.Generation != request.Generation || existing.KubeNode != request.KubeNode { return ReleaseProgress{}, false, ErrReleaseProgressConflict }
		return existing, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) { return ReleaseProgress{}, false, fmt.Errorf("read release progress: %w", err) }
	progress := ReleaseProgress{ID: newStoreID("release"), TaskID: request.TaskID, AttemptID: request.AttemptID, VMID: request.VMID, Generation: request.Generation, KubeNode: request.KubeNode, Step: ReleaseStepNone, State: ReleaseStatePending, UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if _, err := tx.ExecContext(ctx, `INSERT INTO release_progress(id, task_id, attempt_id, vmid, generation, kube_node, step, state, error_summary, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, progress.ID, progress.TaskID, progress.AttemptID, progress.VMID, progress.Generation, progress.KubeNode, progress.Step, progress.State, "", progress.UpdatedAt); err != nil { return ReleaseProgress{}, false, fmt.Errorf("insert release progress: %w", err) }
	if err := tx.Commit(); err != nil { return ReleaseProgress{}, false, fmt.Errorf("commit release progress: %w", err) }
	return progress, true, nil
}

func (s *Store) GetReleaseProgress(ctx context.Context, taskID, attemptID string) (ReleaseProgress, error) {
	progress, err := scanReleaseProgress(s.db.QueryRowContext(ctx, `SELECT id, task_id, attempt_id, vmid, generation, kube_node, step, state, error_summary, updated_at FROM release_progress WHERE task_id = ? AND attempt_id = ?`, taskID, attemptID))
	if errors.Is(err, sql.ErrNoRows) { return ReleaseProgress{}, ErrReleaseProgressNotFound }
	if err != nil { return ReleaseProgress{}, fmt.Errorf("read release progress: %w", err) }
	if err := validateStoredReleaseProgress(progress); err != nil { return ReleaseProgress{}, err }
	return progress, nil
}

// UpdateReleaseProgress permits idempotent checkpoints, forward-only steps,
// and explicit UNKNOWN at the current step. It never infers external progress.
func (s *Store) UpdateReleaseProgress(ctx context.Context, taskID, attemptID, step, state, errorSummary string) error {
	if _, ok := releaseStepOrder[step]; !ok || !validReleaseState(state) || len(errorSummary) > 2048 { return ErrReleaseProgressInvalid }
	current, err := s.GetReleaseProgress(ctx, taskID, attemptID); if err != nil { return err }
	if releaseStepOrder[step] < releaseStepOrder[current.Step] { return ErrReleaseProgressRegress }
	if releaseStepOrder[step] == releaseStepOrder[current.Step] && current.State == ReleaseStateCompleted && state != ReleaseStateCompleted { return ErrReleaseProgressRegress }
	_, err = s.db.ExecContext(ctx, "UPDATE release_progress SET step = ?, state = ?, error_summary = ?, updated_at = ? WHERE task_id = ? AND attempt_id = ?", step, state, errorSummary, time.Now().UTC().Format(time.RFC3339Nano), taskID, attemptID)
	if err != nil { return fmt.Errorf("update release progress: %w", err) }
	return nil
}

func validateReleaseProgressRequest(request ReleaseProgressRequest) error {
	if request.TaskID == "" || request.AttemptID == "" || request.Generation == "" || request.KubeNode == "" || request.VMID < 3000 || request.VMID > 3999 { return ErrReleaseProgressInvalid }
	return nil
}
func validReleaseState(state string) bool { return state == ReleaseStatePending || state == ReleaseStateRunning || state == ReleaseStateUnknown || state == ReleaseStateCompleted }
func validateStoredReleaseProgress(progress ReleaseProgress) error {
	if progress.ID == "" || progress.TaskID == "" || progress.AttemptID == "" || progress.VMID < 3000 || progress.VMID > 3999 || progress.Generation == "" || progress.KubeNode == "" || !validReleaseState(progress.State) { return ErrReleaseProgressInvalid }
	if _, ok := releaseStepOrder[progress.Step]; !ok || progress.UpdatedAt == "" { return ErrReleaseProgressInvalid }
	return nil
}
type releaseProgressScanner interface { Scan(...any) error }
func scanReleaseProgress(scanner releaseProgressScanner) (ReleaseProgress, error) {
	var progress ReleaseProgress
	err := scanner.Scan(&progress.ID, &progress.TaskID, &progress.AttemptID, &progress.VMID, &progress.Generation, &progress.KubeNode, &progress.Step, &progress.State, &progress.ErrorSummary, &progress.UpdatedAt)
	return progress, err
}
