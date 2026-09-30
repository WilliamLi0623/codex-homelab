package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	SessionVMIDMin = 4000
	SessionVMIDMax = 4999
)

var (
	ErrSessionRuntimeBindingNotFound = errors.New("session runtime binding not found")
	ErrSessionRuntimeBindingConflict = errors.New("session runtime binding state conflict")
	ErrSessionRuntimeBindingInvalid  = errors.New("session runtime binding is invalid")
)

type SessionRuntimeBinding struct {
	ID               string
	SessionID        string
	EpochID          string
	RuntimeID        string
	VMID             int
	Generation       string
	State            string
	PendingOperation string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

type SessionRuntimeBindingHistory struct {
	ID               int64
	RuntimeBindingID string
	SessionID        string
	EpochID          string
	RuntimeID        string
	VMID             int
	Generation       string
	FinalState       string
	CreatedAt        time.Time
	DeletedAt        time.Time
}

func (s *Store) CreateSessionRuntimeBinding(ctx context.Context, binding SessionRuntimeBinding) error {
	if strings.TrimSpace(binding.ID) != binding.ID || binding.ID == "" || strings.TrimSpace(binding.SessionID) != binding.SessionID || binding.SessionID == "" || strings.TrimSpace(binding.EpochID) != binding.EpochID || binding.EpochID == "" || binding.VMID < SessionVMIDMin || binding.VMID > SessionVMIDMax || strings.TrimSpace(binding.Generation) != binding.Generation || binding.Generation == "" || binding.State != "ALLOCATING" || binding.CreatedAt.IsZero() || binding.UpdatedAt.IsZero() {
		return fmt.Errorf("%w: id, session, epoch, reserved vmid, generation, ALLOCATING state, and timestamps are required", ErrSessionRuntimeBindingInvalid)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin session runtime binding: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `INSERT INTO session_runtime_bindings (id, session_id, epoch_id, runtime_id, vmid, generation, state, created_at, updated_at, pending_operation)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'create')`, binding.ID, binding.SessionID, binding.EpochID, nullableString(binding.RuntimeID), binding.VMID, binding.Generation, binding.State, formatSessionTime(binding.CreatedAt), formatSessionTime(binding.UpdatedAt)); err != nil {
		return fmt.Errorf("insert session runtime binding: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE session_epochs SET runtime_binding_id = ? WHERE id = ? AND session_id = ?`, binding.ID, binding.EpochID, binding.SessionID); err != nil {
		return fmt.Errorf("link epoch to runtime binding: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit session runtime binding: %w", err)
	}
	return nil
}

// AllocateSessionRuntimeBinding atomically reserves the first available VMID
// in the dedicated interactive-session range and links it to its epoch.
func (s *Store) AllocateSessionRuntimeBinding(ctx context.Context, binding SessionRuntimeBinding) (SessionRuntimeBinding, error) {
	if strings.TrimSpace(binding.ID) != binding.ID || binding.ID == "" || strings.TrimSpace(binding.SessionID) != binding.SessionID || binding.SessionID == "" || strings.TrimSpace(binding.EpochID) != binding.EpochID || binding.EpochID == "" || binding.VMID != 0 || strings.TrimSpace(binding.Generation) != binding.Generation || binding.Generation == "" || binding.State != "ALLOCATING" || binding.CreatedAt.IsZero() || binding.UpdatedAt.IsZero() {
		return SessionRuntimeBinding{}, fmt.Errorf("%w: id, session, epoch, empty vmid, generation, ALLOCATING state, and timestamps are required", ErrSessionRuntimeBindingInvalid)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SessionRuntimeBinding{}, fmt.Errorf("begin session runtime allocation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `SELECT vmid FROM session_runtime_bindings WHERE vmid BETWEEN ? AND ? AND state != 'DELETED'`, SessionVMIDMin, SessionVMIDMax)
	if err != nil {
		return SessionRuntimeBinding{}, fmt.Errorf("read reserved session VMIDs: %w", err)
	}
	reserved := make(map[int]struct{})
	for rows.Next() {
		var vmid int
		if err := rows.Scan(&vmid); err != nil {
			_ = rows.Close()
			return SessionRuntimeBinding{}, fmt.Errorf("scan reserved session VMID: %w", err)
		}
		reserved[vmid] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return SessionRuntimeBinding{}, fmt.Errorf("iterate reserved session VMIDs: %w", err)
	}
	if err := rows.Close(); err != nil {
		return SessionRuntimeBinding{}, fmt.Errorf("close reserved session VMIDs: %w", err)
	}
	for vmid := SessionVMIDMin; vmid <= SessionVMIDMax; vmid++ {
		if _, exists := reserved[vmid]; exists {
			continue
		}
		binding.VMID = vmid
		_, err = tx.ExecContext(ctx, `INSERT INTO session_runtime_bindings (id, session_id, epoch_id, runtime_id, vmid, generation, state, created_at, updated_at, pending_operation)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'create')`, binding.ID, binding.SessionID, binding.EpochID, nullableString(binding.RuntimeID), binding.VMID, binding.Generation, binding.State, formatSessionTime(binding.CreatedAt), formatSessionTime(binding.UpdatedAt))
		if err != nil {
			return SessionRuntimeBinding{}, fmt.Errorf("reserve session VMID %d: %w", vmid, err)
		}
		result, err := tx.ExecContext(ctx, `UPDATE session_epochs SET runtime_binding_id = ? WHERE id = ? AND session_id = ?`, binding.ID, binding.EpochID, binding.SessionID)
		if err != nil {
			return SessionRuntimeBinding{}, fmt.Errorf("link allocated runtime to epoch: %w", err)
		}
		linked, err := result.RowsAffected()
		if err != nil {
			return SessionRuntimeBinding{}, fmt.Errorf("read allocated runtime epoch link count: %w", err)
		}
		if linked != 1 {
			return SessionRuntimeBinding{}, fmt.Errorf("%w: epoch %q is not linked to session %q", ErrSessionRuntimeBindingInvalid, binding.EpochID, binding.SessionID)
		}
		if err := tx.Commit(); err != nil {
			return SessionRuntimeBinding{}, fmt.Errorf("commit session runtime allocation: %w", err)
		}
		return binding, nil
	}
	return SessionRuntimeBinding{}, fmt.Errorf("%w: reserved VMID range %d-%d is exhausted", ErrSessionRuntimeBindingConflict, SessionVMIDMin, SessionVMIDMax)
}

func (s *Store) GetSessionRuntimeBinding(ctx context.Context, sessionID, epochID string) (SessionRuntimeBinding, error) {
	return s.getSessionRuntimeBinding(ctx, `SELECT id, session_id, epoch_id, runtime_id, vmid, generation, state, created_at, updated_at, pending_operation
		FROM session_runtime_bindings WHERE session_id = ? AND epoch_id = ?`, sessionID, epochID)
}

func (s *Store) GetSessionRuntimeBindingByVMID(ctx context.Context, vmid int) (SessionRuntimeBinding, error) {
	return s.getSessionRuntimeBinding(ctx, `SELECT id, session_id, epoch_id, runtime_id, vmid, generation, state, created_at, updated_at, pending_operation
		FROM session_runtime_bindings WHERE vmid = ? AND state != 'DELETED'`, vmid)
}

func (s *Store) ListSessionRuntimeBindingHistory(ctx context.Context, sessionID, epochID string) ([]SessionRuntimeBindingHistory, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, runtime_binding_id, session_id, epoch_id, runtime_id, vmid, generation, final_state, created_at, deleted_at
		FROM session_runtime_binding_history WHERE session_id = ? AND epoch_id = ? ORDER BY id`, sessionID, epochID)
	if err != nil {
		return nil, fmt.Errorf("query Session runtime binding history: %w", err)
	}
	defer rows.Close()
	var history []SessionRuntimeBindingHistory
	for rows.Next() {
		var entry SessionRuntimeBindingHistory
		var runtimeID sql.NullString
		var createdAt, deletedAt string
		if err := rows.Scan(&entry.ID, &entry.RuntimeBindingID, &entry.SessionID, &entry.EpochID, &runtimeID, &entry.VMID, &entry.Generation, &entry.FinalState, &createdAt, &deletedAt); err != nil {
			return nil, fmt.Errorf("scan Session runtime binding history: %w", err)
		}
		entry.RuntimeID = runtimeID.String
		if entry.CreatedAt, err = parseSessionTime(createdAt); err != nil {
			return nil, fmt.Errorf("parse runtime history created_at: %w", err)
		}
		if entry.DeletedAt, err = parseSessionTime(deletedAt); err != nil {
			return nil, fmt.Errorf("parse runtime history deleted_at: %w", err)
		}
		history = append(history, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate Session runtime binding history: %w", err)
	}
	return history, nil
}

func (s *Store) getSessionRuntimeBinding(ctx context.Context, query string, args ...any) (SessionRuntimeBinding, error) {
	var binding SessionRuntimeBinding
	var runtimeID sql.NullString
	var createdAt, updatedAt string
	err := s.db.QueryRowContext(ctx, query, args...).Scan(&binding.ID, &binding.SessionID, &binding.EpochID, &runtimeID, &binding.VMID, &binding.Generation, &binding.State, &createdAt, &updatedAt, &binding.PendingOperation)
	if errors.Is(err, sql.ErrNoRows) {
		return SessionRuntimeBinding{}, ErrSessionRuntimeBindingNotFound
	}
	if err != nil {
		return SessionRuntimeBinding{}, fmt.Errorf("read session runtime binding: %w", err)
	}
	binding.RuntimeID = runtimeID.String
	if binding.CreatedAt, err = parseSessionTime(createdAt); err != nil {
		return SessionRuntimeBinding{}, fmt.Errorf("parse runtime binding created_at: %w", err)
	}
	if binding.UpdatedAt, err = parseSessionTime(updatedAt); err != nil {
		return SessionRuntimeBinding{}, fmt.Errorf("parse runtime binding updated_at: %w", err)
	}
	return binding, nil
}

func (s *Store) UpdateSessionRuntimeBindingState(ctx context.Context, id, expectedState, nextState, runtimeID string, updatedAt time.Time) error {
	if id == "" || !validSessionRuntimeBindingState(expectedState) || !validSessionRuntimeBindingState(nextState) || updatedAt.IsZero() {
		return fmt.Errorf("%w: id, valid states, and updated_at are required", ErrSessionRuntimeBindingInvalid)
	}
	if !validSessionRuntimeTransition(expectedState, nextState) {
		return fmt.Errorf("%w: transition %s -> %s is not allowed", ErrSessionRuntimeBindingInvalid, expectedState, nextState)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin runtime binding state update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if expectedState != "DELETED" && nextState == "DELETED" {
		var binding SessionRuntimeBinding
		var createdAt string
		var oldRuntimeID sql.NullString
		err := tx.QueryRowContext(ctx, `SELECT id, session_id, epoch_id, runtime_id, vmid, generation, state, created_at
			FROM session_runtime_bindings WHERE id = ? AND state = ?`, id, expectedState).Scan(&binding.ID, &binding.SessionID, &binding.EpochID, &oldRuntimeID, &binding.VMID, &binding.Generation, &binding.State, &createdAt)
		if errors.Is(err, sql.ErrNoRows) {
			return s.runtimeBindingConflictOrNotFound(ctx, tx, id)
		}
		if err != nil {
			return fmt.Errorf("read runtime binding before archival: %w", err)
		}
		if binding.VMID > 0 {
			if _, err := tx.ExecContext(ctx, `INSERT INTO session_runtime_binding_history (runtime_binding_id, session_id, epoch_id, runtime_id, vmid, generation, final_state, created_at, deleted_at)
				VALUES (?, ?, ?, ?, ?, ?, 'DELETED', ?, ?)`, binding.ID, binding.SessionID, binding.EpochID, oldRuntimeID, binding.VMID, binding.Generation, createdAt, formatSessionTime(updatedAt)); err != nil {
				return fmt.Errorf("archive deleted runtime binding: %w", err)
			}
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE session_runtime_bindings SET state = ?, runtime_id = CASE WHEN ? = '' THEN runtime_id ELSE ? END,
		pending_operation = CASE WHEN ? = 'UNKNOWN' THEN pending_operation ELSE ? END, updated_at = ?
		WHERE id = ? AND state = ?`, nextState, runtimeID, runtimeID, nextState, pendingOperationForState(nextState), formatSessionTime(updatedAt), id, expectedState)
	if err != nil {
		return fmt.Errorf("update session runtime binding state: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read runtime binding update count: %w", err)
	}
	if updated == 1 {
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit runtime binding state update: %w", err)
		}
		return nil
	}
	return s.runtimeBindingConflictOrNotFound(ctx, tx, id)
}

func pendingOperationForState(state string) string {
	switch state {
	case "ALLOCATING":
		return "create"
	case "CREATING":
		return "create"
	case "STARTING", "RESUMING":
		return "start"
	case "STOPPING":
		return "stop"
	case "REPLACING":
		return "replace"
	case "DELETING":
		return "delete"
	default:
		return ""
	}
}

// CompleteSessionRuntimeReplacement archives the deleted generation and keeps
// its VMID reserved while the replacement is allocated, avoiding a reuse gap.
func (s *Store) CompleteSessionRuntimeReplacement(ctx context.Context, id, generation string, updatedAt time.Time) error {
	if id == "" || strings.TrimSpace(generation) != generation || generation == "" || updatedAt.IsZero() {
		return fmt.Errorf("%w: binding id, new generation, and updated_at are required", ErrSessionRuntimeBindingInvalid)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin Session runtime replacement completion: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var binding SessionRuntimeBinding
	var runtimeID sql.NullString
	var createdAt string
	err = tx.QueryRowContext(ctx, `SELECT id, session_id, epoch_id, runtime_id, vmid, generation, state, created_at
		FROM session_runtime_bindings WHERE id = ? AND state = 'DELETING'`, id).Scan(&binding.ID, &binding.SessionID, &binding.EpochID, &runtimeID, &binding.VMID, &binding.Generation, &binding.State, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return s.runtimeBindingConflictOrNotFound(ctx, tx, id)
	}
	if err != nil {
		return fmt.Errorf("read deleted Session runtime generation: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO session_runtime_binding_history (runtime_binding_id, session_id, epoch_id, runtime_id, vmid, generation, final_state, created_at, deleted_at)
		VALUES (?, ?, ?, ?, ?, ?, 'DELETED', ?, ?)`, binding.ID, binding.SessionID, binding.EpochID, runtimeID, binding.VMID, binding.Generation, createdAt, formatSessionTime(updatedAt)); err != nil {
		return fmt.Errorf("archive replaced Session runtime generation: %w", err)
	}
	result, err := tx.ExecContext(ctx, `UPDATE session_runtime_bindings SET generation = ?, runtime_id = NULL, state = 'ALLOCATING', pending_operation = 'create', updated_at = ?
		WHERE id = ? AND state = 'DELETING'`, generation, formatSessionTime(updatedAt), id)
	if err != nil {
		return fmt.Errorf("reserve Session VMID for replacement: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read Session runtime replacement update count: %w", err)
	}
	if updated != 1 {
		return s.runtimeBindingConflictOrNotFound(ctx, tx, id)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit Session runtime replacement: %w", err)
	}
	return nil
}

func (s *Store) runtimeBindingConflictOrNotFound(ctx context.Context, tx *sql.Tx, id string) error {
	var exists int
	if err := tx.QueryRowContext(ctx, "SELECT 1 FROM session_runtime_bindings WHERE id = ?", id).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
		return ErrSessionRuntimeBindingNotFound
	} else if err != nil {
		return fmt.Errorf("check runtime binding after state conflict: %w", err)
	}
	return ErrSessionRuntimeBindingConflict
}

func validSessionRuntimeBindingState(state string) bool {
	switch state {
	case "ALLOCATING", "CREATING", "STARTING", "READY", "STOPPING", "STOPPED", "RESUMING", "REPLACING", "UNKNOWN", "FAILED", "DELETING", "DELETED":
		return true
	default:
		return false
	}
}

func validSessionRuntimeTransition(from, to string) bool {
	if from == to {
		return true
	}
	allowed := map[string]map[string]bool{
		"ALLOCATING": {"CREATING": true, "UNKNOWN": true, "FAILED": true},
		"CREATING":   {"STARTING": true, "UNKNOWN": true, "FAILED": true},
		"STARTING":   {"READY": true, "UNKNOWN": true, "FAILED": true},
		"READY":      {"STOPPING": true, "REPLACING": true, "UNKNOWN": true},
		"STOPPING":   {"STOPPED": true, "UNKNOWN": true},
		"STOPPED":    {"RESUMING": true, "REPLACING": true, "DELETING": true, "UNKNOWN": true},
		"RESUMING":   {"READY": true, "STOPPED": true, "UNKNOWN": true, "FAILED": true},
		"REPLACING":  {"CREATING": true, "UNKNOWN": true, "FAILED": true},
		"UNKNOWN":    {"CREATING": true, "STARTING": true, "READY": true, "STOPPING": true, "STOPPED": true, "REPLACING": true, "FAILED": true, "DELETING": true, "DELETED": true},
		"FAILED":     {"DELETING": true},
		"DELETING":   {"DELETED": true, "UNKNOWN": true},
	}
	return allowed[from][to]
}
