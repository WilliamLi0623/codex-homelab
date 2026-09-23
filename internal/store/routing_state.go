package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/WilliamLi0623/codex-homelab/internal/domain"
)

var (
	ErrRoutingStateNotFound  = errors.New("routing state not found")
	ErrInvalidRoutingState   = errors.New("invalid routing state")
	ErrStaleRoutingState     = errors.New("stale routing state")
	ErrAttemptRouteNotFound  = errors.New("attempt route snapshot not found")
	ErrAttemptRouteImmutable = errors.New("attempt route snapshot is immutable")
)

// RoutingState is the authenticated host-published quota mode. It contains
// no account identity, credentials, provider key, or model secret.
type RoutingState struct {
	Mode       string
	ObservedAt time.Time
	Generation int64
}

// AttemptRouteSnapshot is the complete non-secret route selected for one
// attempt. SecretName and SecretKey are references, never secret values.
type AttemptRouteSnapshot struct {
	Mode            string
	Generation      int64
	Role            string
	Provider        string
	Model           string
	WireAPI         string
	ReasoningEffort string
	BaseURL         string
	SecretName      string
	SecretKey       string
}

var (
	storeSecretName = regexp.MustCompile(`^[a-z0-9](?:[-a-z0-9]*[a-z0-9])?(?:\.[a-z0-9](?:[-a-z0-9]*[a-z0-9])?)*$`)
	storeSecretKey  = regexp.MustCompile(`^[A-Za-z0-9._-]{1,253}$`)
)

func (s *Store) SetRoutingState(ctx context.Context, state RoutingState) error {
	if err := validateRoutingState(state); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin routing state update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var current RoutingState
	var observed string
	err = tx.QueryRowContext(ctx, "SELECT mode, observed_at, generation FROM routing_state WHERE id = 1").Scan(&current.Mode, &observed, &current.Generation)
	if errors.Is(err, sql.ErrNoRows) {
		if _, err := tx.ExecContext(ctx, "INSERT INTO routing_state(id, mode, observed_at, generation) VALUES (1, ?, ?, ?)", state.Mode, state.ObservedAt.Format(time.RFC3339Nano), state.Generation); err != nil {
			return fmt.Errorf("insert routing state: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("read routing state: %w", err)
	} else {
		current.ObservedAt, err = time.Parse(time.RFC3339Nano, observed)
		if err != nil {
			return fmt.Errorf("parse persisted routing state: %w", err)
		}
		if state.Generation < current.Generation || state.ObservedAt.Before(current.ObservedAt) || (state.Generation == current.Generation && !sameRoutingState(state, current)) {
			return ErrStaleRoutingState
		}
		if state == current {
			return tx.Commit()
		}
		if _, err := tx.ExecContext(ctx, "UPDATE routing_state SET mode = ?, observed_at = ?, generation = ? WHERE id = 1", state.Mode, state.ObservedAt.Format(time.RFC3339Nano), state.Generation); err != nil {
			return fmt.Errorf("update routing state: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit routing state: %w", err)
	}
	return nil
}

func sameRoutingState(left, right RoutingState) bool {
	return left.Mode == right.Mode && left.Generation == right.Generation && left.ObservedAt.Equal(right.ObservedAt)
}

func (s *Store) GetRoutingState(ctx context.Context) (RoutingState, error) {
	var state RoutingState
	var observed string
	if err := s.db.QueryRowContext(ctx, "SELECT mode, observed_at, generation FROM routing_state WHERE id = 1").Scan(&state.Mode, &observed, &state.Generation); errors.Is(err, sql.ErrNoRows) {
		return RoutingState{}, ErrRoutingStateNotFound
	} else if err != nil {
		return RoutingState{}, fmt.Errorf("read routing state: %w", err)
	}
	parsed, err := time.Parse(time.RFC3339Nano, observed)
	if err != nil {
		return RoutingState{}, fmt.Errorf("parse routing state observed_at: %w", err)
	}
	state.ObservedAt = parsed
	return state, nil
}

func validateRoutingState(state RoutingState) error {
	if state.Mode != "normal" && state.Mode != "quota_fallback" {
		return fmt.Errorf("%w: unsupported mode %q", ErrInvalidRoutingState, state.Mode)
	}
	if state.Generation < 1 || state.ObservedAt.IsZero() {
		return fmt.Errorf("%w: generation and observed_at are required", ErrInvalidRoutingState)
	}
	return nil
}

func validateAttemptRoute(route AttemptRouteSnapshot) error {
	if route.Mode != "normal" && route.Mode != "quota_fallback" {
		return fmt.Errorf("%w: unsupported mode %q", ErrInvalidRoutingState, route.Mode)
	}
	if route.Generation < 1 || route.Role == "" || route.Provider == "" || route.Model == "" || route.WireAPI == "" || route.ReasoningEffort == "" {
		return fmt.Errorf("%w: incomplete route snapshot", ErrInvalidRoutingState)
	}
	u, err := url.Parse(route.BaseURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("%w: invalid route base URL", ErrInvalidRoutingState)
	}
	if len(route.SecretName) > 253 || !storeSecretName.MatchString(route.SecretName) || strings.TrimSpace(route.SecretName) != route.SecretName || !validSecretNameLabels(route.SecretName) || !storeSecretKey.MatchString(route.SecretKey) || strings.TrimSpace(route.SecretKey) != route.SecretKey {
		return fmt.Errorf("%w: invalid secret reference", ErrInvalidRoutingState)
	}
	if strings.Contains(strings.ToLower(route.SecretName), "bearer ") || strings.Contains(strings.ToLower(route.SecretKey), "bearer ") {
		return fmt.Errorf("%w: secret value is not a reference", ErrInvalidRoutingState)
	}
	return nil
}

func validSecretNameLabels(name string) bool {
	for _, label := range strings.Split(name, ".") {
		if len(label) > 63 {
			return false
		}
	}
	return true
}

// GetAttemptRoute returns a snapshot only for attempts created with the new
// route-aware API. Legacy attempts return ErrAttemptRouteNotFound.
func (s *Store) GetAttemptRoute(ctx context.Context, taskID, attemptID string) (AttemptRouteSnapshot, error) {
	return scanAttemptRoute(s.db.QueryRowContext(ctx, `SELECT route_mode, route_generation, route_role, route_provider, route_model, route_wire_api, route_reasoning_effort, route_base_url, route_secret_name, route_secret_key FROM task_attempts WHERE task_id = ? AND id = ?`, taskID, attemptID))
}

func scanAttemptRoute(row *sql.Row) (AttemptRouteSnapshot, error) {
	var route AttemptRouteSnapshot
	var mode, role, provider, model, wire, effort, baseURL, secretName, secretKey sql.NullString
	var generation sql.NullInt64
	if err := row.Scan(&mode, &generation, &role, &provider, &model, &wire, &effort, &baseURL, &secretName, &secretKey); errors.Is(err, sql.ErrNoRows) {
		return AttemptRouteSnapshot{}, ErrAttemptRouteNotFound
	} else if err != nil {
		return AttemptRouteSnapshot{}, fmt.Errorf("read attempt route: %w", err)
	}
	if !mode.Valid || !generation.Valid || !role.Valid || !provider.Valid || !model.Valid || !wire.Valid || !effort.Valid || !baseURL.Valid || !secretName.Valid || !secretKey.Valid {
		return AttemptRouteSnapshot{}, ErrAttemptRouteNotFound
	}
	route = AttemptRouteSnapshot{Mode: mode.String, Generation: generation.Int64, Role: role.String, Provider: provider.String, Model: model.String, WireAPI: wire.String, ReasoningEffort: effort.String, BaseURL: baseURL.String, SecretName: secretName.String, SecretKey: secretKey.String}
	return route, nil
}

// ReplaceAttemptRoute is intentionally unavailable: route snapshots are
// written only in the same transaction as attempt creation.
func (s *Store) ReplaceAttemptRoute(context.Context, string, string, AttemptRouteSnapshot) error {
	return ErrAttemptRouteImmutable
}

// StartAttemptWithRoute creates an attempt and its route snapshot atomically.
func (s *Store) StartAttemptWithRoute(ctx context.Context, taskID, role string, route AttemptRouteSnapshot) (domain.Task, domain.Attempt, error) {
	if err := validateAttemptRoute(route); err != nil {
		return domain.Task{}, domain.Attempt{}, err
	}
	if route.Role != role {
		return domain.Task{}, domain.Attempt{}, fmt.Errorf("%w: route role %q does not match requested role %q", ErrInvalidRoutingState, route.Role, role)
	}
	return s.startAttempt(ctx, taskID, role, &route)
}
