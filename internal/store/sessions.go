package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrSessionNotFound = errors.New("session not found")
	ErrSessionInvalid  = errors.New("session is invalid")
)

type Session struct {
	ID                string
	Title             string
	Repository        string
	WorkspaceID       string
	State             string
	PreferredBackend  string
	AutomaticFailover bool
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

type SessionEpoch struct {
	ID               string
	SessionID        string
	Sequence         int
	Backend          string
	IdentityID       string
	RuntimeBindingID string
	ProviderThreadID string
	State            string
	StartedAt        time.Time
	EndedAt          time.Time
}

func (s *Store) CreateSession(ctx context.Context, session Session) error {
	if strings.TrimSpace(session.ID) != session.ID || session.ID == "" || strings.TrimSpace(session.Title) == "" || !validSessionState(session.State) || !validSessionBackend(session.PreferredBackend) {
		return fmt.Errorf("%w: id, title, state, and supported preferred backend are required", ErrSessionInvalid)
	}
	if session.CreatedAt.IsZero() || session.UpdatedAt.IsZero() {
		return fmt.Errorf("%w: created_at and updated_at are required", ErrSessionInvalid)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin session creation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `INSERT INTO sessions (id, title, repository_id, workspace_id, state, preferred_backend, automatic_failover, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, session.ID, session.Title, nullableString(session.Repository), nullableString(session.WorkspaceID), session.State, session.PreferredBackend, boolInt(session.AutomaticFailover), formatSessionTime(session.CreatedAt), formatSessionTime(session.UpdatedAt)); err != nil {
		return fmt.Errorf("insert session: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO session_routing_policies (session_id, preferred_backend, automatic_failover, automatic_failback, cross_account_allowed, updated_at)
		VALUES (?, ?, ?, 0, 0, ?)`, session.ID, session.PreferredBackend, boolInt(session.AutomaticFailover), formatSessionTime(session.UpdatedAt)); err != nil {
		return fmt.Errorf("insert session routing policy: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit session creation: %w", err)
	}
	return nil
}

func (s *Store) GetSession(ctx context.Context, id string) (Session, error) {
	var session Session
	var repository, workspace sql.NullString
	var automaticFailover int
	var createdAt, updatedAt string
	err := s.db.QueryRowContext(ctx, `SELECT id, title, repository_id, workspace_id, state, preferred_backend, automatic_failover, created_at, updated_at
		FROM sessions WHERE id = ?`, id).Scan(&session.ID, &session.Title, &repository, &workspace, &session.State, &session.PreferredBackend, &automaticFailover, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrSessionNotFound
	}
	if err != nil {
		return Session{}, fmt.Errorf("read session: %w", err)
	}
	session.Repository = repository.String
	session.WorkspaceID = workspace.String
	session.AutomaticFailover = automaticFailover != 0
	if session.CreatedAt, err = parseSessionTime(createdAt); err != nil {
		return Session{}, fmt.Errorf("parse session created_at: %w", err)
	}
	if session.UpdatedAt, err = parseSessionTime(updatedAt); err != nil {
		return Session{}, fmt.Errorf("parse session updated_at: %w", err)
	}
	return session, nil
}

func (s *Store) CreateSessionEpoch(ctx context.Context, epoch SessionEpoch) error {
	if strings.TrimSpace(epoch.ID) != epoch.ID || epoch.ID == "" || strings.TrimSpace(epoch.SessionID) != epoch.SessionID || epoch.SessionID == "" || epoch.Sequence < 1 || !validSessionBackend(epoch.Backend) || strings.TrimSpace(epoch.IdentityID) != epoch.IdentityID || epoch.IdentityID == "" || !validSessionEpochState(epoch.State) {
		return fmt.Errorf("%w: epoch identity, sequence, backend, and state are required", ErrSessionInvalid)
	}
	var startedAt, endedAt any
	if !epoch.StartedAt.IsZero() {
		startedAt = formatSessionTime(epoch.StartedAt)
	}
	if !epoch.EndedAt.IsZero() {
		endedAt = formatSessionTime(epoch.EndedAt)
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO session_epochs (id, session_id, sequence, backend, identity_id, runtime_binding_id, provider_thread_id, state, started_at, ended_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, epoch.ID, epoch.SessionID, epoch.Sequence, epoch.Backend, epoch.IdentityID, nullableString(epoch.RuntimeBindingID), nullableString(epoch.ProviderThreadID), epoch.State, startedAt, endedAt)
	if err != nil {
		return fmt.Errorf("insert session epoch: %w", err)
	}
	return nil
}

func (s *Store) ListSessionEpochs(ctx context.Context, sessionID string) ([]SessionEpoch, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, session_id, sequence, backend, identity_id, runtime_binding_id, provider_thread_id, state, started_at, ended_at
		FROM session_epochs WHERE session_id = ? ORDER BY sequence`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("list session epochs: %w", err)
	}
	defer rows.Close()

	epochs := make([]SessionEpoch, 0)
	for rows.Next() {
		var epoch SessionEpoch
		var runtimeBinding, providerThread, startedAt, endedAt sql.NullString
		if err := rows.Scan(&epoch.ID, &epoch.SessionID, &epoch.Sequence, &epoch.Backend, &epoch.IdentityID, &runtimeBinding, &providerThread, &epoch.State, &startedAt, &endedAt); err != nil {
			return nil, fmt.Errorf("scan session epoch: %w", err)
		}
		epoch.RuntimeBindingID, epoch.ProviderThreadID = runtimeBinding.String, providerThread.String
		if startedAt.Valid {
			epoch.StartedAt, err = parseSessionTime(startedAt.String)
			if err != nil {
				return nil, fmt.Errorf("parse session epoch started_at: %w", err)
			}
		}
		if endedAt.Valid {
			epoch.EndedAt, err = parseSessionTime(endedAt.String)
			if err != nil {
				return nil, fmt.Errorf("parse session epoch ended_at: %w", err)
			}
		}
		epochs = append(epochs, epoch)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate session epochs: %w", err)
	}
	return epochs, nil
}

func validSessionBackend(backend string) bool {
	return backend == "codex-a" || backend == "codex-b" || backend == "spark-glm"
}

func validSessionState(state string) bool {
	switch state {
	case "CREATED", "PROVISIONING", "READY", "ACTIVE", "DRAINING", "SWITCHING", "STOPPED", "ERROR", "UNKNOWN", "DELETING", "DELETED":
		return true
	default:
		return false
	}
}

func validSessionEpochState(state string) bool {
	switch state {
	case "STARTING", "ACTIVE", "DRAIN_REQUESTED", "DRAINING", "HANDOFF_GENERATING", "HANDOFF_READY", "STOPPED", "FAILED", "UNKNOWN":
		return true
	default:
		return false
	}
}

func formatSessionTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}

func parseSessionTime(value string) (time.Time, error) {
	return time.Parse(time.RFC3339Nano, value)
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
