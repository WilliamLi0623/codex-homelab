package store

import (
	"context"
	"fmt"
)

// ModelProfile contains only public routing metadata. Credentials stay in the
// private runtime secret store and are never represented here.
type ModelProfile struct {
	ID       string
	Provider string
	ModelID  string
	Enabled  bool
}

func (s *Store) UpsertModelProfile(ctx context.Context, profile ModelProfile) error {
	if profile.ID == "" || profile.Provider == "" || profile.ModelID == "" {
		return fmt.Errorf("model profile id, provider, and model ID are required")
	}
	enabled := 0
	if profile.Enabled {
		enabled = 1
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO model_profiles(id, provider, model_id, enabled) VALUES (?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET provider = excluded.provider, model_id = excluded.model_id, enabled = excluded.enabled`, profile.ID, profile.Provider, profile.ModelID, enabled); err != nil {
		return fmt.Errorf("upsert model profile: %w", err)
	}
	return nil
}

func (s *Store) ListModelProfiles(ctx context.Context) ([]ModelProfile, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id, provider, model_id, enabled FROM model_profiles ORDER BY id")
	if err != nil {
		return nil, fmt.Errorf("list model profiles: %w", err)
	}
	defer rows.Close()
	var profiles []ModelProfile
	for rows.Next() {
		var profile ModelProfile
		var enabled int
		if err := rows.Scan(&profile.ID, &profile.Provider, &profile.ModelID, &enabled); err != nil {
			return nil, fmt.Errorf("scan model profile: %w", err)
		}
		profile.Enabled = enabled != 0
		profiles = append(profiles, profile)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate model profiles: %w", err)
	}
	return profiles, nil
}
