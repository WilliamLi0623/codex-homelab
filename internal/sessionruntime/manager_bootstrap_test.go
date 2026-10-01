package sessionruntime

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/WilliamLi0623/codex-homelab/internal/store"
)

type managerBootstrapDriver struct {
	applyCalls int
	applyErr   error
	cancel     context.CancelFunc
}

func (d *managerBootstrapDriver) VerifyIdentity(context.Context, store.SessionRuntimeBinding) error {
	return nil
}
func (d *managerBootstrapDriver) Apply(context.Context, store.SessionRuntimeBinding, store.SessionBootstrapStage) (store.SessionBootstrapEvidence, error) {
	d.applyCalls++
	if d.cancel != nil {
		d.cancel()
	}
	return store.SessionBootstrapEvidence{SHA256: strings.Repeat("a", 64)}, d.applyErr
}
func (d *managerBootstrapDriver) Observe(context.Context, store.SessionRuntimeBinding, store.SessionBootstrapStage) (store.SessionBootstrapEvidence, bool, error) {
	return store.SessionBootstrapEvidence{}, false, nil
}

func TestManagerBootstrapCreateAndResume(t *testing.T) {
	runtime := &fakeRuntime{available: true}
	db, manager := setupManager(t, runtime)
	driver := &managerBootstrapDriver{}
	coordinator, err := NewBootstrapCoordinator(db, driver)
	if err != nil {
		t.Fatal(err)
	}
	manager.config.Bootstrap = coordinator
	ctx := context.Background()
	binding, err := manager.Create(ctx, CreateRequest{ID: "binding-a", SessionID: "session-a", EpochID: "epoch-a", Generation: "gen-1"})
	if err != nil || binding.State != "READY" || driver.applyCalls != 8 || runtime.readyCalls != 1 {
		t.Fatalf("create: state=%s applies=%d ready=%d error=%v", binding.State, driver.applyCalls, runtime.readyCalls, err)
	}
	if err := manager.Stop(ctx, "session-a", "epoch-a"); err != nil {
		t.Fatal(err)
	}
	if err := manager.Resume(ctx, "session-a", "epoch-a"); err != nil {
		t.Fatal(err)
	}
	if driver.applyCalls != 8 || runtime.readyCalls != 2 {
		t.Fatalf("resume replayed bootstrap or skipped readiness: apply=%d ready=%d", driver.applyCalls, runtime.readyCalls)
	}
}

func TestManagerBootstrapUnknownCannotReplayOrBecomeReady(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(map[bool]string{false: "driver failure", true: "cancellation"}[canceled], func(t *testing.T) {
			runtime := &fakeRuntime{available: true}
			db, manager := setupManager(t, runtime)
			driver := &managerBootstrapDriver{applyErr: errors.New("sensitive upstream value")}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if canceled {
				driver.cancel = cancel
			}
			coordinator, err := NewBootstrapCoordinator(db, driver)
			if err != nil {
				t.Fatal(err)
			}
			manager.config.Bootstrap = coordinator
			_, err = manager.Create(ctx, CreateRequest{ID: "binding-a", SessionID: "session-a", EpochID: "epoch-a", Generation: "gen-1"})
			if !errors.Is(err, ErrOutcomeUnknown) || strings.Contains(err.Error(), "sensitive upstream value") {
				t.Fatalf("unsafe create error: %v", err)
			}
			binding, err := db.GetSessionRuntimeBinding(context.Background(), "session-a", "epoch-a")
			if err != nil || binding.State != "UNKNOWN" {
				t.Fatalf("binding not durable UNKNOWN: %+v error=%v", binding, err)
			}
			if _, err := manager.Reconcile(context.Background(), "session-a", "epoch-a"); err == nil {
				t.Fatal("incomplete bootstrap accepted READY")
			}
			if driver.applyCalls != 1 || runtime.readyCalls != 0 || runtime.cloneCalls != 1 || runtime.startCalls != 1 {
				t.Fatalf("unsafe replay/readiness: apply=%d ready=%d clone=%d start=%d", driver.applyCalls, runtime.readyCalls, runtime.cloneCalls, runtime.startCalls)
			}
		})
	}
}

func TestManagerBootstrapCompletionDoesNotReplaceAccountReadiness(t *testing.T) {
	runtime := &fakeRuntime{available: true, readyErr: errors.New("account acceptance pending")}
	db, manager := setupManager(t, runtime)
	driver := &managerBootstrapDriver{}
	coordinator, err := NewBootstrapCoordinator(db, driver)
	if err != nil {
		t.Fatal(err)
	}
	manager.config.Bootstrap = coordinator
	binding, err := manager.Create(context.Background(), CreateRequest{ID: "binding-a", SessionID: "session-a", EpochID: "epoch-a", Generation: "gen-1"})
	if !errors.Is(err, ErrOutcomeUnknown) || binding.State != "UNKNOWN" || driver.applyCalls != 8 || runtime.readyCalls != 1 {
		t.Fatalf("bootstrap bypassed readiness: binding=%+v error=%v", binding, err)
	}
}

func TestManagerBootstrapResumeMissingCheckpointsFailsClosed(t *testing.T) {
	runtime := &fakeRuntime{available: true}
	db, manager := setupManager(t, runtime)
	ctx := context.Background()
	if _, err := manager.Create(ctx, CreateRequest{ID: "binding-a", SessionID: "session-a", EpochID: "epoch-a", Generation: "gen-1"}); err != nil {
		t.Fatal(err)
	}
	if err := manager.Stop(ctx, "session-a", "epoch-a"); err != nil {
		t.Fatal(err)
	}
	driver := &managerBootstrapDriver{}
	coordinator, err := NewBootstrapCoordinator(db, driver)
	if err != nil {
		t.Fatal(err)
	}
	manager.config.Bootstrap = coordinator
	if err := manager.Resume(ctx, "session-a", "epoch-a"); !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("resume error=%v", err)
	}
	binding, err := db.GetSessionRuntimeBinding(ctx, "session-a", "epoch-a")
	if err != nil || binding.State != "UNKNOWN" || driver.applyCalls != 0 || runtime.readyCalls != 1 {
		t.Fatalf("resume bypassed missing checkpoints: binding=%+v applies=%d ready=%d error=%v", binding, driver.applyCalls, runtime.readyCalls, err)
	}
}

func TestManagerRejectsBootstrapFromDifferentStore(t *testing.T) {
	runtime := &fakeRuntime{available: true}
	db, manager := setupManager(t, runtime)
	other, _ := setupManager(t, &fakeRuntime{available: true})
	coordinator, err := NewBootstrapCoordinator(other, &managerBootstrapDriver{})
	if err != nil {
		t.Fatal(err)
	}
	config := manager.config
	config.Bootstrap = coordinator
	if _, err := NewManager(db, runtime, config); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("cross-store bootstrap accepted: %v", err)
	}
}
