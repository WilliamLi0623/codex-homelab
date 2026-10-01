package sessionruntime

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/WilliamLi0623/codex-homelab/internal/store"
)

const bootstrapTestSHA256 = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

type bootstrapDriverFuncs struct {
	mu           sync.Mutex
	applyCalls   []store.SessionBootstrapStage
	observeCalls []store.SessionBootstrapStage
	verified     int
	apply        func(context.Context, store.SessionRuntimeBinding, store.SessionBootstrapStage) (store.SessionBootstrapEvidence, error)
	observe      func(context.Context, store.SessionRuntimeBinding, store.SessionBootstrapStage) (store.SessionBootstrapEvidence, bool, error)
	verify       func(context.Context, store.SessionRuntimeBinding) error
}

func (d *bootstrapDriverFuncs) Apply(ctx context.Context, binding store.SessionRuntimeBinding, stage store.SessionBootstrapStage) (store.SessionBootstrapEvidence, error) {
	d.mu.Lock()
	d.applyCalls = append(d.applyCalls, stage)
	d.mu.Unlock()
	if d.apply != nil {
		return d.apply(ctx, binding, stage)
	}
	return store.SessionBootstrapEvidence{SHA256: bootstrapTestSHA256}, nil
}

func (d *bootstrapDriverFuncs) Observe(ctx context.Context, binding store.SessionRuntimeBinding, stage store.SessionBootstrapStage) (store.SessionBootstrapEvidence, bool, error) {
	d.mu.Lock()
	d.observeCalls = append(d.observeCalls, stage)
	d.mu.Unlock()
	if d.observe != nil {
		return d.observe(ctx, binding, stage)
	}
	return store.SessionBootstrapEvidence{}, false, nil
}

func (d *bootstrapDriverFuncs) VerifyIdentity(ctx context.Context, binding store.SessionRuntimeBinding) error {
	d.mu.Lock()
	d.verified++
	d.mu.Unlock()
	if d.verify != nil {
		return d.verify(ctx, binding)
	}
	return nil
}

func (d *bootstrapDriverFuncs) calls() (apply, observe []store.SessionBootstrapStage, verified int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]store.SessionBootstrapStage(nil), d.applyCalls...), append([]store.SessionBootstrapStage(nil), d.observeCalls...), d.verified
}

func newBootstrapFixture(t *testing.T) (*store.Store, store.SessionRuntimeBinding) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "bootstrap.sqlite"))
	if err != nil {
		t.Fatalf("store.Open(): %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	now := time.Now().UTC()
	if err := db.CreateSession(context.Background(), store.Session{ID: "session-1", Title: "Bootstrap", State: "READY", PreferredBackend: "codex-a", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("CreateSession(): %v", err)
	}
	if err := db.CreateSessionEpoch(context.Background(), store.SessionEpoch{ID: "epoch-1", SessionID: "session-1", Sequence: 1, Backend: "codex-a", IdentityID: "identity-1", State: "ACTIVE", StartedAt: now}); err != nil {
		t.Fatalf("CreateSessionEpoch(): %v", err)
	}
	binding := store.SessionRuntimeBinding{ID: "binding-1", SessionID: "session-1", EpochID: "epoch-1", VMID: 4010, Generation: "generation-1", State: "ALLOCATING", CreatedAt: now, UpdatedAt: now}
	if err := db.CreateSessionRuntimeBinding(context.Background(), binding); err != nil {
		t.Fatalf("CreateSessionRuntimeBinding(): %v", err)
	}
	return db, binding
}

func TestBootstrapEnsurePersistsIntentBeforeApplyingEveryOrderedStage(t *testing.T) {
	db, binding := newBootstrapFixture(t)
	var coordinator *BootstrapCoordinator
	driver := &bootstrapDriverFuncs{apply: func(ctx context.Context, got store.SessionRuntimeBinding, stage store.SessionBootstrapStage) (store.SessionBootstrapEvidence, error) {
		checkpoint, err := db.GetSessionBootstrapStage(ctx, got.ID, got.Generation, stage)
		if err != nil || checkpoint.Status != store.SessionBootstrapIntent {
			t.Errorf("Apply(%q) observed checkpoint (%+v, %v), want durable INTENT", stage, checkpoint, err)
		}
		return store.SessionBootstrapEvidence{SHA256: bootstrapTestSHA256}, nil
	}}
	var err error
	coordinator, err = NewBootstrapCoordinator(db, driver)
	if err != nil {
		t.Fatalf("NewBootstrapCoordinator(): %v", err)
	}
	if err := coordinator.Ensure(context.Background(), binding); err != nil {
		t.Fatalf("Ensure(): %v", err)
	}
	want := []store.SessionBootstrapStage{
		store.SessionBootstrapIsolation,
		store.SessionBootstrapGuestIdentity,
		store.SessionBootstrapHostPin,
		store.SessionBootstrapImageBackup,
		store.SessionBootstrapImageSanitized,
		store.SessionBootstrapNetworkEnabled,
		store.SessionBootstrapArtifactVerified,
		store.SessionBootstrapTransportVerified,
	}
	apply, observe, _ := driver.calls()
	if fmt.Sprint(apply) != fmt.Sprint(want) || len(observe) != 0 {
		t.Fatalf("driver calls apply=%v observe=%v, want apply=%v only", apply, observe, want)
	}
	for _, stage := range want {
		checkpoint, err := db.GetSessionBootstrapStage(context.Background(), binding.ID, binding.Generation, stage)
		if err != nil || checkpoint.Status != store.SessionBootstrapComplete || checkpoint.Evidence.SHA256 != bootstrapTestSHA256 {
			t.Errorf("checkpoint %q = (%+v, %v), want complete evidence", stage, checkpoint, err)
		}
	}
}

func TestBootstrapEnsureReconcilesPersistedIntentByObservationOnly(t *testing.T) {
	db, binding := newBootstrapFixture(t)
	if _, claimed, err := db.BeginSessionBootstrapStage(context.Background(), binding.ID, binding.Generation, store.SessionBootstrapIsolation); err != nil || !claimed {
		t.Fatalf("BeginSessionBootstrapStage() = claimed %t, err %v", claimed, err)
	}
	driver := &bootstrapDriverFuncs{observe: func(context.Context, store.SessionRuntimeBinding, store.SessionBootstrapStage) (store.SessionBootstrapEvidence, bool, error) {
		return store.SessionBootstrapEvidence{SHA256: bootstrapTestSHA256}, true, nil
	}}
	coordinator, err := NewBootstrapCoordinator(db, driver)
	if err != nil {
		t.Fatalf("NewBootstrapCoordinator(): %v", err)
	}
	if err := coordinator.Ensure(context.Background(), binding); err != nil {
		t.Fatalf("Ensure(): %v", err)
	}
	apply, observe, _ := driver.calls()
	wantApply := []store.SessionBootstrapStage{
		store.SessionBootstrapGuestIdentity,
		store.SessionBootstrapHostPin,
		store.SessionBootstrapImageBackup,
		store.SessionBootstrapImageSanitized,
		store.SessionBootstrapNetworkEnabled,
		store.SessionBootstrapArtifactVerified,
		store.SessionBootstrapTransportVerified,
	}
	if fmt.Sprint(apply) != fmt.Sprint(wantApply) || fmt.Sprint(observe) != fmt.Sprint([]store.SessionBootstrapStage{store.SessionBootstrapIsolation}) {
		t.Fatalf("driver calls apply=%v observe=%v, want no replay of isolation and ordered new stages", apply, observe)
	}
	checkpoint, err := db.GetSessionBootstrapStage(context.Background(), binding.ID, binding.Generation, store.SessionBootstrapIsolation)
	if err != nil || checkpoint.Status != store.SessionBootstrapComplete {
		t.Fatalf("reconciled checkpoint = (%+v, %v), want COMPLETE", checkpoint, err)
	}
}

func TestBootstrapEnsureNeverReappliesTimedOutSideEffect(t *testing.T) {
	db, binding := newBootstrapFixture(t)
	for run := 0; run < 2; run++ {
		driver := &bootstrapDriverFuncs{apply: func(context.Context, store.SessionRuntimeBinding, store.SessionBootstrapStage) (store.SessionBootstrapEvidence, error) {
			return store.SessionBootstrapEvidence{}, errors.New("side effect timed out")
		}}
		coordinator, err := NewBootstrapCoordinator(db, driver)
		if err != nil {
			t.Fatalf("NewBootstrapCoordinator(): %v", err)
		}
		if err := coordinator.Ensure(context.Background(), binding); !errors.Is(err, ErrOutcomeUnknown) {
			t.Fatalf("Ensure() error = %v, want ErrOutcomeUnknown", err)
		}
		apply, observe, _ := driver.calls()
		if run == 0 && (len(apply) != 1 || len(observe) != 0) {
			t.Fatalf("first Ensure() calls apply=%v observe=%v, want one Apply", apply, observe)
		}
		if run == 1 && (len(apply) != 0 || fmt.Sprint(observe) != fmt.Sprint([]store.SessionBootstrapStage{store.SessionBootstrapIsolation})) {
			t.Fatalf("restart Ensure() calls apply=%v observe=%v, want observe only", apply, observe)
		}
	}
}

func TestBootstrapEnsureCancellationPreservesUnknownCheckpoint(t *testing.T) {
	db, binding := newBootstrapFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	driver := &bootstrapDriverFuncs{apply: func(context.Context, store.SessionRuntimeBinding, store.SessionBootstrapStage) (store.SessionBootstrapEvidence, error) {
		cancel()
		return store.SessionBootstrapEvidence{}, context.Canceled
	}}
	coordinator, err := NewBootstrapCoordinator(db, driver)
	if err != nil {
		t.Fatalf("NewBootstrapCoordinator(): %v", err)
	}
	if err := coordinator.Ensure(ctx, binding); !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("Ensure() error = %v, want ErrOutcomeUnknown", err)
	}
	checkpoint, err := db.GetSessionBootstrapStage(context.Background(), binding.ID, binding.Generation, store.SessionBootstrapIsolation)
	if err != nil || checkpoint.Status != store.SessionBootstrapUnknown {
		t.Fatalf("cancelled checkpoint = (%+v, %v), want UNKNOWN", checkpoint, err)
	}
}

func TestBootstrapEnsureInvalidEvidencePreservesUnknown(t *testing.T) {
	db, binding := newBootstrapFixture(t)
	driver := &bootstrapDriverFuncs{apply: func(context.Context, store.SessionRuntimeBinding, store.SessionBootstrapStage) (store.SessionBootstrapEvidence, error) {
		return store.SessionBootstrapEvidence{SHA256: "not-a-digest"}, nil
	}}
	coordinator, err := NewBootstrapCoordinator(db, driver)
	if err != nil {
		t.Fatalf("NewBootstrapCoordinator(): %v", err)
	}
	if err := coordinator.Ensure(context.Background(), binding); err == nil || !strings.Contains(err.Error(), "invalid_evidence") {
		t.Fatalf("Ensure() error = %v, want invalid_evidence stage error", err)
	}
	checkpoint, err := db.GetSessionBootstrapStage(context.Background(), binding.ID, binding.Generation, store.SessionBootstrapIsolation)
	if err != nil || checkpoint.Status != store.SessionBootstrapUnknown {
		t.Fatalf("invalid-evidence checkpoint = (%+v, %v), want UNKNOWN", checkpoint, err)
	}
}

func TestBootstrapEnsureRejectsStaleBindingBeforeDriverCalls(t *testing.T) {
	db, binding := newBootstrapFixture(t)
	driver := &bootstrapDriverFuncs{}
	coordinator, err := NewBootstrapCoordinator(db, driver)
	if err != nil {
		t.Fatalf("NewBootstrapCoordinator(): %v", err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*store.SessionRuntimeBinding)
	}{
		{name: "session", mutate: func(b *store.SessionRuntimeBinding) { b.SessionID = "other-session" }},
		{name: "epoch", mutate: func(b *store.SessionRuntimeBinding) { b.EpochID = "other-epoch" }},
		{name: "binding ID", mutate: func(b *store.SessionRuntimeBinding) { b.ID = "other-binding" }},
		{name: "generation", mutate: func(b *store.SessionRuntimeBinding) { b.Generation = "replaced-generation" }},
		{name: "VMID", mutate: func(b *store.SessionRuntimeBinding) { b.VMID++ }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stale := binding
			tc.mutate(&stale)
			if err := coordinator.Ensure(context.Background(), stale); !errors.Is(err, ErrBootstrapBinding) {
				t.Fatalf("Ensure(mismatched %s) error = %v, want ErrBootstrapBinding", tc.name, err)
			}
		})
	}
	apply, observe, verified := driver.calls()
	if len(apply) != 0 || len(observe) != 0 || verified != 0 {
		t.Fatalf("driver calls apply=%v observe=%v verified=%d, want none", apply, observe, verified)
	}
}

func TestBootstrapEnsureRedactsDriverIdentityErrors(t *testing.T) {
	db, binding := newBootstrapFixture(t)
	secret := "private-guest-credential"
	driver := &bootstrapDriverFuncs{verify: func(context.Context, store.SessionRuntimeBinding) error {
		return errors.New("identity probe failed with " + secret)
	}}
	coordinator, err := NewBootstrapCoordinator(db, driver)
	if err != nil {
		t.Fatalf("NewBootstrapCoordinator(): %v", err)
	}
	err = coordinator.Ensure(context.Background(), binding)
	if err == nil || strings.Contains(err.Error(), secret) || !strings.Contains(err.Error(), "driver_identity") {
		t.Fatalf("Ensure() error = %v, want sanitized driver_identity class", err)
	}
	checkpoint, err := db.GetSessionBootstrapStage(context.Background(), binding.ID, binding.Generation, store.SessionBootstrapIsolation)
	if err != nil || checkpoint.Status != store.SessionBootstrapUnknown {
		t.Fatalf("identity failure checkpoint = (%+v, %v), want UNKNOWN", checkpoint, err)
	}
	apply, observe, _ := driver.calls()
	if len(apply) != 0 || len(observe) != 0 {
		t.Fatalf("identity failure called driver action: apply=%v observe=%v", apply, observe)
	}
}

func TestBootstrapEnsureRedactsDriverErrorsAndKeepsUnknown(t *testing.T) {
	db, binding := newBootstrapFixture(t)
	secret := "access-token-should-not-appear"
	driver := &bootstrapDriverFuncs{apply: func(context.Context, store.SessionRuntimeBinding, store.SessionBootstrapStage) (store.SessionBootstrapEvidence, error) {
		return store.SessionBootstrapEvidence{}, errors.New("failed using " + secret)
	}}
	coordinator, err := NewBootstrapCoordinator(db, driver)
	if err != nil {
		t.Fatalf("NewBootstrapCoordinator(): %v", err)
	}
	err = coordinator.Ensure(context.Background(), binding)
	if err == nil || strings.Contains(err.Error(), secret) || !strings.Contains(err.Error(), string(store.SessionBootstrapIsolation)) || !strings.Contains(err.Error(), "driver") {
		t.Fatalf("Ensure() error = %v, want stage and driver class without secret", err)
	}
	checkpoint, err := db.GetSessionBootstrapStage(context.Background(), binding.ID, binding.Generation, store.SessionBootstrapIsolation)
	if err != nil || checkpoint.Status != store.SessionBootstrapUnknown {
		t.Fatalf("failed checkpoint = (%+v, %v), want UNKNOWN", checkpoint, err)
	}
}

func TestBootstrapEnsureCompletesAllStagesWithoutImplyingAccountReadiness(t *testing.T) {
	db, binding := newBootstrapFixture(t)
	driver := &bootstrapDriverFuncs{}
	coordinator, err := NewBootstrapCoordinator(db, driver)
	if err != nil {
		t.Fatalf("NewBootstrapCoordinator(): %v", err)
	}
	if err := coordinator.Ensure(context.Background(), binding); err != nil {
		t.Fatalf("Ensure(): %v", err)
	}
	got, err := db.GetSessionRuntimeBinding(context.Background(), binding.SessionID, binding.EpochID)
	if err != nil || got.State != "ALLOCATING" {
		t.Fatalf("runtime binding after transport bootstrap = (%+v, %v), want unchanged ALLOCATING state", got, err)
	}
}

func TestBootstrapEnsureConcurrentCallsDoNotDuplicateActions(t *testing.T) {
	db, binding := newBootstrapFixture(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	driver := &bootstrapDriverFuncs{apply: func(context.Context, store.SessionRuntimeBinding, store.SessionBootstrapStage) (store.SessionBootstrapEvidence, error) {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
		return store.SessionBootstrapEvidence{SHA256: bootstrapTestSHA256}, nil
	}}
	coordinator, err := NewBootstrapCoordinator(db, driver)
	if err != nil {
		t.Fatalf("NewBootstrapCoordinator(): %v", err)
	}
	results := make(chan error, 2)
	go func() { results <- coordinator.Ensure(context.Background(), binding) }()
	<-entered
	go func() { results <- coordinator.Ensure(context.Background(), binding) }()
	close(release)
	for i := 0; i < 2; i++ {
		if err := <-results; err != nil {
			t.Fatalf("Ensure() #%d error = %v", i+1, err)
		}
	}
	apply, observe, _ := driver.calls()
	if fmt.Sprint(apply) != fmt.Sprint([]store.SessionBootstrapStage{
		store.SessionBootstrapIsolation,
		store.SessionBootstrapGuestIdentity,
		store.SessionBootstrapHostPin,
		store.SessionBootstrapImageBackup,
		store.SessionBootstrapImageSanitized,
		store.SessionBootstrapNetworkEnabled,
		store.SessionBootstrapArtifactVerified,
		store.SessionBootstrapTransportVerified,
	}) || len(observe) != 0 {
		t.Fatalf("concurrent driver calls apply=%v observe=%v, want one ordered action per stage", apply, observe)
	}
}

func TestBootstrapReconcileMissingCheckpointFailsBeforeProvisioning(t *testing.T) {
	db, binding := newBootstrapFixture(t)
	if _, claimed, err := db.BeginSessionBootstrapStage(context.Background(), binding.ID, binding.Generation, store.SessionBootstrapIsolation); err != nil || !claimed {
		t.Fatalf("BeginSessionBootstrapStage(isolation) = claimed %t, err %v", claimed, err)
	}
	if err := db.CompleteSessionBootstrapStage(context.Background(), binding.ID, binding.Generation, store.SessionBootstrapIsolation, store.SessionBootstrapIntent, store.SessionBootstrapEvidence{SHA256: bootstrapTestSHA256}); err != nil {
		t.Fatalf("CompleteSessionBootstrapStage(isolation): %v", err)
	}
	driver := &bootstrapDriverFuncs{}
	coordinator, err := NewBootstrapCoordinator(db, driver)
	if err != nil {
		t.Fatalf("NewBootstrapCoordinator(): %v", err)
	}
	if err := coordinator.Reconcile(context.Background(), binding); !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("Reconcile() error = %v, want ErrOutcomeUnknown for missing guest_identity checkpoint", err)
	}
	apply, observe, _ := driver.calls()
	if len(apply) != 0 || len(observe) != 0 {
		t.Fatalf("missing-step reconciliation called driver: apply=%v observe=%v", apply, observe)
	}
	if _, err := db.GetSessionBootstrapStage(context.Background(), binding.ID, binding.Generation, store.SessionBootstrapGuestIdentity); !errors.Is(err, store.ErrSessionBootstrapNotFound) {
		t.Fatalf("missing guest_identity checkpoint lookup error = %v, want still missing", err)
	}
}

func TestBootstrapReconcileObservesExistingIntentWithoutApplying(t *testing.T) {
	db, binding := newBootstrapFixture(t)
	for _, stage := range []store.SessionBootstrapStage{
		store.SessionBootstrapIsolation,
		store.SessionBootstrapGuestIdentity,
		store.SessionBootstrapHostPin,
		store.SessionBootstrapImageBackup,
		store.SessionBootstrapImageSanitized,
		store.SessionBootstrapNetworkEnabled,
		store.SessionBootstrapArtifactVerified,
	} {
		if _, claimed, err := db.BeginSessionBootstrapStage(context.Background(), binding.ID, binding.Generation, stage); err != nil || !claimed {
			t.Fatalf("BeginSessionBootstrapStage(%q) = claimed %t, err %v", stage, claimed, err)
		}
		if err := db.CompleteSessionBootstrapStage(context.Background(), binding.ID, binding.Generation, stage, store.SessionBootstrapIntent, store.SessionBootstrapEvidence{SHA256: bootstrapTestSHA256}); err != nil {
			t.Fatalf("CompleteSessionBootstrapStage(%q): %v", stage, err)
		}
	}
	if _, claimed, err := db.BeginSessionBootstrapStage(context.Background(), binding.ID, binding.Generation, store.SessionBootstrapTransportVerified); err != nil || !claimed {
		t.Fatalf("BeginSessionBootstrapStage(transport_verified) = claimed %t, err %v", claimed, err)
	}
	driver := &bootstrapDriverFuncs{observe: func(_ context.Context, _ store.SessionRuntimeBinding, stage store.SessionBootstrapStage) (store.SessionBootstrapEvidence, bool, error) {
		return store.SessionBootstrapEvidence{SHA256: bootstrapTestSHA256}, stage == store.SessionBootstrapTransportVerified, nil
	}}
	coordinator, err := NewBootstrapCoordinator(db, driver)
	if err != nil {
		t.Fatalf("NewBootstrapCoordinator(): %v", err)
	}
	if err := coordinator.Reconcile(context.Background(), binding); err != nil {
		t.Fatalf("Reconcile(): %v", err)
	}
	apply, observe, _ := driver.calls()
	if len(apply) != 0 || fmt.Sprint(observe) != fmt.Sprint([]store.SessionBootstrapStage{store.SessionBootstrapTransportVerified}) {
		t.Fatalf("driver calls apply=%v observe=%v, want observe-only for existing intent", apply, observe)
	}
}

func TestNewBootstrapCoordinatorRejectsNilDependencies(t *testing.T) {
	db, _ := newBootstrapFixture(t)
	if _, err := NewBootstrapCoordinator(db, nil); err == nil {
		t.Fatal("NewBootstrapCoordinator(db, nil) succeeded, want fail-closed error")
	}
}
