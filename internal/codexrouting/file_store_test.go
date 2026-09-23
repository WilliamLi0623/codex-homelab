package codexrouting

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestFileRoutingStateStorePersistsAtomicallyAndRejectsStaleState(t *testing.T) {
	store := &FileRoutingStateStore{Path: filepath.Join(t.TempDir(), "state", "routing.json")}
	first := RoutingState{Mode: ModeNormal, Generation: 1, ObservedAt: time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)}
	if err := store.Save(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	loaded, ok, err := store.Load(context.Background())
	if err != nil || !ok || loaded != first {
		t.Fatalf("Load() = %+v, %t, %v; want %+v, true, nil", loaded, ok, err, first)
	}
	if err := store.Save(context.Background(), first); err != nil {
		t.Fatalf("idempotent Save() error = %v", err)
	}
	conflicting := first
	conflicting.Mode = ModeQuotaFallback
	if err := store.Save(context.Background(), conflicting); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("conflicting same-generation Save() error = %v", err)
	}
	next := RoutingState{Mode: ModeQuotaFallback, Generation: 2, ObservedAt: first.ObservedAt.Add(time.Second)}
	if err := store.Save(context.Background(), next); err != nil {
		t.Fatalf("higher-generation Save() error = %v", err)
	}
	loaded, ok, err = store.Load(context.Background())
	if err != nil || !ok || loaded != next {
		t.Fatalf("Load() after transition = %+v, %t, %v; want %+v", loaded, ok, err, next)
	}
}

func TestFileRoutingStateStoreContainsNoSecretsAndUsesPrivateMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "routing.json")
	store := &FileRoutingStateStore{Path: path}
	state := RoutingState{Mode: ModeNormal, Generation: 1, ObservedAt: time.Now().UTC()}
	if err := store.Save(context.Background(), state); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "token") || strings.Contains(string(data), "secret") {
		t.Fatalf("routing state unexpectedly contains credential-like data: %s", data)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatalf("file permissions = %o, want 600", info.Mode().Perm())
	}
}

func TestFileRoutingStateStoreRejectsMalformedAndOversizedFiles(t *testing.T) {
	for _, contents := range []string{
		`{"mode":"mystery","generation":1,"observed_at":"2026-09-23T12:00:00Z"}`,
		`{"mode":"normal","generation":1,"observed_at":"2026-09-23T12:00:00Z","token":"must-not-accept"}`,
		strings.Repeat(" ", 4097),
	} {
		path := filepath.Join(t.TempDir(), "routing.json")
		if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := (&FileRoutingStateStore{Path: path}).Load(context.Background()); err == nil {
			t.Fatalf("Load() accepted malformed state %q", contents[:min(len(contents), 80)])
		}
	}
}
