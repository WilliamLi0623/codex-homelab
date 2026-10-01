package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

type SessionBootstrapStage string

const (
	SessionBootstrapIsolation        SessionBootstrapStage = "isolation"
	SessionBootstrapGuestIdentity    SessionBootstrapStage = "guest_identity"
	SessionBootstrapHostPin          SessionBootstrapStage = "host_pin"
	SessionBootstrapImageBackup      SessionBootstrapStage = "image_backup_created"
	SessionBootstrapImageSanitized   SessionBootstrapStage = "image_sanitized"
	SessionBootstrapNetworkEnabled   SessionBootstrapStage = "network_enabled"
	SessionBootstrapArtifactVerified SessionBootstrapStage = "artifact_verified"
	// Transport verification does not establish account provisioning or identity.
	SessionBootstrapTransportVerified SessionBootstrapStage = "transport_verified"
)

var sessionBootstrapStages = []SessionBootstrapStage{
	SessionBootstrapIsolation,
	SessionBootstrapGuestIdentity,
	SessionBootstrapHostPin,
	SessionBootstrapImageBackup,
	SessionBootstrapImageSanitized,
	SessionBootstrapNetworkEnabled,
	SessionBootstrapArtifactVerified,
	SessionBootstrapTransportVerified,
}

type SessionBootstrapStatus string

const (
	SessionBootstrapIntent   SessionBootstrapStatus = "INTENT"
	SessionBootstrapUnknown  SessionBootstrapStatus = "UNKNOWN"
	SessionBootstrapComplete SessionBootstrapStatus = "COMPLETE"
)

var (
	ErrSessionBootstrapNotFound = errors.New("session bootstrap checkpoint not found")
	ErrSessionBootstrapConflict = errors.New("session bootstrap checkpoint conflict")
	ErrSessionBootstrapInvalid  = errors.New("session bootstrap input is invalid")
	bootstrapSHA256Pattern      = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// SessionBootstrapEvidence contains only a public evidence digest. Raw output,
// credentials, prompts, and arbitrary operator messages are not accepted.
type SessionBootstrapEvidence struct {
	SHA256 string
}

type SessionBootstrapCheckpoint struct {
	RuntimeBindingID string
	Generation       string
	Stage            SessionBootstrapStage
	Status           SessionBootstrapStatus
	Evidence         SessionBootstrapEvidence
}

// BeginSessionBootstrapStage durably claims one ordered stage. newlyClaimed is
// true only for the single caller that inserts the stage intent.
func (s *Store) BeginSessionBootstrapStage(ctx context.Context, bindingID, generation string, stage SessionBootstrapStage) (SessionBootstrapCheckpoint, bool, error) {
	if err := validateSessionBootstrapKey(bindingID, generation, stage); err != nil {
		return SessionBootstrapCheckpoint{}, false, err
	}
	if err := s.validateCurrentBootstrapBinding(ctx, bindingID, generation); err != nil {
		return SessionBootstrapCheckpoint{}, false, err
	}
	existing, err := s.GetSessionBootstrapStage(ctx, bindingID, generation, stage)
	if err == nil {
		if err := s.validateCurrentBootstrapBinding(ctx, bindingID, generation); err != nil {
			return SessionBootstrapCheckpoint{}, false, err
		}
		return existing, false, nil
	}
	if !errors.Is(err, ErrSessionBootstrapNotFound) {
		return SessionBootstrapCheckpoint{}, false, err
	}
	predecessor, hasPredecessor := sessionBootstrapPredecessor(stage)
	query := `INSERT INTO session_bootstrap_checkpoints (runtime_binding_id, generation, stage, status, evidence_sha256)
		SELECT ?, ?, ?, 'INTENT', '' WHERE EXISTS (
			SELECT 1 FROM session_runtime_bindings WHERE id = ? AND generation = ? AND state != 'DELETED'
		)`
	args := []any{bindingID, generation, stage, bindingID, generation}
	if hasPredecessor {
		query += ` AND EXISTS (SELECT 1 FROM session_bootstrap_checkpoints WHERE runtime_binding_id = ? AND generation = ? AND stage = ? AND status = 'COMPLETE')`
		args = append(args, bindingID, generation, predecessor)
	}
	query += ` ON CONFLICT(runtime_binding_id, generation, stage) DO NOTHING`
	result, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		if bindingErr := s.validateCurrentBootstrapBinding(ctx, bindingID, generation); bindingErr != nil {
			return SessionBootstrapCheckpoint{}, false, bindingErr
		}
		if checkpoint, readErr := s.GetSessionBootstrapStage(ctx, bindingID, generation, stage); readErr == nil {
			return checkpoint, false, nil
		}
		return SessionBootstrapCheckpoint{}, false, fmt.Errorf("claim Session bootstrap stage: %w", err)
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return SessionBootstrapCheckpoint{}, false, fmt.Errorf("read Session bootstrap claim result: %w", err)
	}
	if inserted == 1 {
		checkpoint, err := s.GetSessionBootstrapStage(ctx, bindingID, generation, stage)
		return checkpoint, true, err
	}
	if err := s.validateCurrentBootstrapBinding(ctx, bindingID, generation); err != nil {
		return SessionBootstrapCheckpoint{}, false, err
	}
	checkpoint, err := s.GetSessionBootstrapStage(ctx, bindingID, generation, stage)
	if err == nil {
		return checkpoint, false, nil
	}
	if !errors.Is(err, ErrSessionBootstrapNotFound) {
		return SessionBootstrapCheckpoint{}, false, err
	}
	if hasPredecessor {
		var completed int
		if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM session_bootstrap_checkpoints WHERE runtime_binding_id = ? AND generation = ? AND stage = ? AND status = 'COMPLETE'`, bindingID, generation, predecessor).Scan(&completed); errors.Is(err, sql.ErrNoRows) {
			return SessionBootstrapCheckpoint{}, false, fmt.Errorf("%w: predecessor stage %q is not complete", ErrSessionBootstrapConflict, predecessor)
		} else if err != nil {
			return SessionBootstrapCheckpoint{}, false, fmt.Errorf("read Session bootstrap predecessor: %w", err)
		}
	}
	return SessionBootstrapCheckpoint{}, false, fmt.Errorf("%w: bootstrap stage claim was not inserted", ErrSessionBootstrapConflict)
}

// MarkSessionBootstrapStageUnknown records an outcome that cannot safely be
// replayed. Only an existing intent can be changed to UNKNOWN.
func (s *Store) MarkSessionBootstrapStageUnknown(ctx context.Context, bindingID, generation string, stage SessionBootstrapStage, expected SessionBootstrapStatus) error {
	if err := validateSessionBootstrapKey(bindingID, generation, stage); err != nil {
		return err
	}
	if expected != SessionBootstrapIntent {
		return fmt.Errorf("%w: UNKNOWN can only be recorded from INTENT", ErrSessionBootstrapInvalid)
	}
	return s.updateSessionBootstrapStage(ctx, bindingID, generation, stage, expected, SessionBootstrapUnknown, "")
}

// CompleteSessionBootstrapStage completes an intent or resolves UNKNOWN with
// a positive readback. UNKNOWN is never reset to INTENT.
func (s *Store) CompleteSessionBootstrapStage(ctx context.Context, bindingID, generation string, stage SessionBootstrapStage, expected SessionBootstrapStatus, evidence SessionBootstrapEvidence) error {
	if err := validateSessionBootstrapKey(bindingID, generation, stage); err != nil {
		return err
	}
	if expected != SessionBootstrapIntent && expected != SessionBootstrapUnknown {
		return fmt.Errorf("%w: completion requires expected status INTENT or UNKNOWN", ErrSessionBootstrapInvalid)
	}
	if !bootstrapSHA256Pattern.MatchString(evidence.SHA256) {
		return fmt.Errorf("%w: evidence must be a lowercase SHA256 digest", ErrSessionBootstrapInvalid)
	}
	return s.updateSessionBootstrapStage(ctx, bindingID, generation, stage, expected, SessionBootstrapComplete, evidence.SHA256)
}

// GetSessionBootstrapStage reads checkpoints by generation, including evidence
// from a replaced generation so that past results remain recoverable.
func (s *Store) GetSessionBootstrapStage(ctx context.Context, bindingID, generation string, stage SessionBootstrapStage) (SessionBootstrapCheckpoint, error) {
	if err := validateSessionBootstrapKey(bindingID, generation, stage); err != nil {
		return SessionBootstrapCheckpoint{}, err
	}
	var checkpoint SessionBootstrapCheckpoint
	var digest string
	err := s.db.QueryRowContext(ctx, `SELECT runtime_binding_id, generation, stage, status, evidence_sha256 FROM session_bootstrap_checkpoints WHERE runtime_binding_id = ? AND generation = ? AND stage = ?`, bindingID, generation, stage).Scan(&checkpoint.RuntimeBindingID, &checkpoint.Generation, &checkpoint.Stage, &checkpoint.Status, &digest)
	if errors.Is(err, sql.ErrNoRows) {
		return SessionBootstrapCheckpoint{}, ErrSessionBootstrapNotFound
	}
	if err != nil {
		return SessionBootstrapCheckpoint{}, fmt.Errorf("read Session bootstrap checkpoint: %w", err)
	}
	checkpoint.Evidence.SHA256 = digest
	return checkpoint, nil
}

func (s *Store) updateSessionBootstrapStage(ctx context.Context, bindingID, generation string, stage SessionBootstrapStage, expected, next SessionBootstrapStatus, digest string) error {
	if err := s.validateCurrentBootstrapBinding(ctx, bindingID, generation); err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE session_bootstrap_checkpoints SET status = ?, evidence_sha256 = ?
		WHERE runtime_binding_id = ? AND generation = ? AND stage = ? AND status = ?
		AND EXISTS (SELECT 1 FROM session_runtime_bindings WHERE id = ? AND generation = ? AND state != 'DELETED')`, next, digest, bindingID, generation, stage, expected, bindingID, generation)
	if err != nil {
		return fmt.Errorf("update Session bootstrap checkpoint: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read Session bootstrap update count: %w", err)
	}
	if updated == 1 {
		return nil
	}
	if err := s.validateCurrentBootstrapBinding(ctx, bindingID, generation); err != nil {
		return err
	}
	var exists int
	err = s.db.QueryRowContext(ctx, `SELECT 1 FROM session_bootstrap_checkpoints WHERE runtime_binding_id = ? AND generation = ? AND stage = ?`, bindingID, generation, stage).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrSessionBootstrapNotFound
	}
	if err != nil {
		return fmt.Errorf("check Session bootstrap checkpoint after conflict: %w", err)
	}
	return ErrSessionBootstrapConflict
}

func (s *Store) validateCurrentBootstrapBinding(ctx context.Context, bindingID, generation string) error {
	var currentGeneration, state string
	err := s.db.QueryRowContext(ctx, `SELECT generation, state FROM session_runtime_bindings WHERE id = ?`, bindingID).Scan(&currentGeneration, &state)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrSessionBootstrapNotFound
	}
	if err != nil {
		return fmt.Errorf("read Session runtime binding for bootstrap: %w", err)
	}
	if state == "DELETED" || currentGeneration != generation {
		return ErrSessionBootstrapConflict
	}
	return nil
}

func validateSessionBootstrapKey(bindingID, generation string, stage SessionBootstrapStage) error {
	if strings.TrimSpace(bindingID) != bindingID || bindingID == "" || strings.TrimSpace(generation) != generation || generation == "" || !validSessionBootstrapStage(stage) {
		return fmt.Errorf("%w: binding ID, generation, and a supported stage are required", ErrSessionBootstrapInvalid)
	}
	return nil
}

func validSessionBootstrapStage(stage SessionBootstrapStage) bool {
	for _, supported := range sessionBootstrapStages {
		if stage == supported {
			return true
		}
	}
	return false
}

func sessionBootstrapPredecessor(stage SessionBootstrapStage) (SessionBootstrapStage, bool) {
	for i, current := range sessionBootstrapStages {
		if current == stage && i > 0 {
			return sessionBootstrapStages[i-1], true
		}
	}
	return "", false
}
