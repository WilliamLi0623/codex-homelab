package store

import (
	"context"
	"testing"
)

func TestUpsertModelProfilePersistsNonSecretDiscoveryState(t *testing.T) {
	database := newTestStore(t)
	profile := ModelProfile{ID: "muse-failover", Provider: "cch", ModelID: "muse-spark-1.3-contributor", Enabled: true}
	if err := database.UpsertModelProfile(context.Background(), profile); err != nil {
		t.Fatalf("UpsertModelProfile() error = %v", err)
	}
	if err := database.UpsertModelProfile(context.Background(), ModelProfile{ID: "glm-flash-worker", Provider: "cch", ModelID: "glm-5.3-flash", Enabled: false}); err != nil {
		t.Fatalf("UpsertModelProfile() disabled profile error = %v", err)
	}

	profiles, err := database.ListModelProfiles(context.Background())
	if err != nil {
		t.Fatalf("ListModelProfiles() error = %v", err)
	}
	if len(profiles) != 2 {
		t.Fatalf("profiles = %+v, want two profiles", profiles)
	}
	byID := map[string]ModelProfile{}
	for _, persisted := range profiles {
		byID[persisted.ID] = persisted
	}
	if byID["muse-failover"] != profile || byID["glm-flash-worker"].Enabled {
		t.Fatalf("profiles = %+v, want persisted enabled Muse and disabled GLM", profiles)
	}
}

func TestUpsertModelProfileUpdatesOnlyThatProfile(t *testing.T) {
	database := newTestStore(t)
	if err := database.UpsertModelProfile(context.Background(), ModelProfile{ID: "muse-failover", Provider: "cch", ModelID: "muse-spark-1.3-contributor", Enabled: false}); err != nil {
		t.Fatalf("initial UpsertModelProfile() error = %v", err)
	}
	if err := database.UpsertModelProfile(context.Background(), ModelProfile{ID: "muse-failover", Provider: "cch", ModelID: "muse-spark-1.3-contributor", Enabled: true}); err != nil {
		t.Fatalf("update UpsertModelProfile() error = %v", err)
	}
	profiles, err := database.ListModelProfiles(context.Background())
	if err != nil || len(profiles) != 1 || !profiles[0].Enabled {
		t.Fatalf("ListModelProfiles() = (%+v, %v), want enabled updated profile", profiles, err)
	}
}
