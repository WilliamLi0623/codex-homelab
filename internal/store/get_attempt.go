package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/WilliamLi0623/codex-homelab/internal/domain"
)

// GetAttempt returns an attempt only when it belongs to taskID.
func (s *Store) GetAttempt(ctx context.Context, taskID, attemptID string) (domain.Attempt, error) {
	attempt, err := getAttempt(ctx, s.db, taskID, attemptID)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Attempt{}, ErrAttemptNotFound
	}
	if err != nil {
		return domain.Attempt{}, err
	}
	return attempt, nil
}
