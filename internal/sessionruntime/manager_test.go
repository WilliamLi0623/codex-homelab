package sessionruntime

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/WilliamLi0623/codex-homelab/internal/store"
)

type fakeRuntime struct {
	available         bool
	availableErr      error
	state             RuntimeState
	cloneErr          error
	startErr          error
	stopErr           error
	deleteErr         error
	verifyErr         error
	workspaceVolumeID string
	cloneCalls        int
	startCalls        int
	stopCalls         int
	deleteCalls       int
	verifyCalls       int
	lastClone         RuntimeRequest
}

func (r *fakeRuntime) TargetAvailable(context.Context, int) (bool, error) {
	return r.available, r.availableErr
}
func (r *fakeRuntime) Clone(_ context.Context, request RuntimeRequest) (string, error) {
	r.cloneCalls++
	r.lastClone = request
	if request.WorkspaceVolumeID != "" {
		r.workspaceVolumeID = request.WorkspaceVolumeID
	} else if r.workspaceVolumeID == "" {
		r.workspaceVolumeID = fmt.Sprintf("pool:subvol-%d-disk-1", request.VMID)
	}
	if r.cloneErr == nil {
		r.state = RuntimeStopped
	}
	if r.cloneErr != nil {
		return "", r.cloneErr
	}
	return r.workspaceVolumeID, nil
}
func (r *fakeRuntime) WorkspaceVolume(context.Context, int) (string, error) {
	return r.workspaceVolumeID, nil
}
func (r *fakeRuntime) DetachWorkspace(_ context.Context, _ int, expected string) error {
	if expected != r.workspaceVolumeID {
		return errors.New("workspace volume mismatch")
	}
	return nil
}
func (r *fakeRuntime) WorkspaceVolumeExists(context.Context, string) (bool, error) {
	return r.workspaceVolumeID != "", nil
}
func (r *fakeRuntime) Start(context.Context, int) error {
	r.startCalls++
	if r.startErr == nil {
		r.state = RuntimeRunning
	}
	return r.startErr
}
func (r *fakeRuntime) Stop(context.Context, int) error {
	r.stopCalls++
	if r.stopErr == nil {
		r.state = RuntimeStopped
	}
	return r.stopErr
}
func (r *fakeRuntime) Observe(context.Context, int) (RuntimeState, error) { return r.state, nil }
func (r *fakeRuntime) VerifyIdentity(context.Context, store.SessionRuntimeBinding) error {
	r.verifyCalls++
	return r.verifyErr
}
func (r *fakeRuntime) Delete(context.Context, int) error {
	r.deleteCalls++
	if r.deleteErr == nil {
		r.state = RuntimeMissing
	}
	return r.deleteErr
}

func setupManager(t *testing.T, runtime Runtime) (*store.Store, *Manager) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "sessions.sqlite"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	now := time.Now().UTC()
	if err := db.CreateSession(context.Background(), store.Session{ID: "session-a", Title: "A", State: "READY", PreferredBackend: "codex-a", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	if err := db.CreateSessionEpoch(context.Background(), store.SessionEpoch{ID: "epoch-a", SessionID: "session-a", Sequence: 1, Backend: "codex-a", IdentityID: "identity-a", State: "ACTIVE", StartedAt: now}); err != nil {
		t.Fatalf("CreateSessionEpoch() error = %v", err)
	}
	manager, err := NewManager(db, runtime, Config{TemplateVMID: 3900, SystemStorage: "local", WorkspaceStorage: "pool", WorkspaceSizeGiB: 32})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	return db, manager
}

func TestCreateStopResumeDeleteSessionRuntimeLifecycle(t *testing.T) {
	runtime := &fakeRuntime{available: true, state: RuntimeMissing}
	db, manager := setupManager(t, runtime)
	ctx := context.Background()
	binding, err := manager.Create(ctx, CreateRequest{ID: "binding-a", SessionID: "session-a", EpochID: "epoch-a", Generation: "gen-1"})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if binding.State != "READY" || binding.VMID != store.SessionVMIDMin || runtime.lastClone.TemplateVMID != 3900 || runtime.lastClone.SystemStorage != "local" {
		t.Fatalf("created binding=%+v clone=%+v, want ready Session runtime on reserved VMID and SSD template config", binding, runtime.lastClone)
	}
	if _, err := manager.Replace(ctx, "session-a", "epoch-a", "gen-1"); !errors.Is(err, ErrInvalidConfig) || runtime.deleteCalls != 0 {
		t.Fatalf("Replace() with reused generation error=%v delete_calls=%d; want validation before mutation", err, runtime.deleteCalls)
	}
	if err := manager.Stop(ctx, "session-a", "epoch-a"); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if err := manager.Resume(ctx, "session-a", "epoch-a"); err != nil {
		t.Fatalf("Resume() error = %v", err)
	}
	replaced, err := manager.Replace(ctx, "session-a", "epoch-a", "gen-2")
	if err != nil {
		t.Fatalf("Replace() error = %v", err)
	}
	if replaced.State != "READY" || replaced.PendingOperation != "" || replaced.Generation != "gen-2" || replaced.VMID < store.SessionVMIDMin || replaced.VMID > store.SessionVMIDMax {
		t.Fatalf("replacement binding = %+v, want ready new generation in Session VMID range", replaced)
	}
	history, err := db.ListSessionRuntimeBindingHistory(ctx, "session-a", "epoch-a")
	if err != nil {
		t.Fatalf("read replaced runtime history: %v", err)
	}
	if len(history) != 1 || history[0].Generation != "gen-1" {
		t.Fatalf("archived generations = %+v, want only gen-1", history)
	}
	if err := manager.Stop(ctx, "session-a", "epoch-a"); err != nil {
		t.Fatalf("Stop(before delete) error = %v", err)
	}
	if err := manager.Delete(ctx, "session-a", "epoch-a"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	got, err := db.GetSessionRuntimeBinding(ctx, "session-a", "epoch-a")
	if err != nil || got.State != "DELETED" || got.PendingOperation != "" {
		t.Fatalf("deleted binding=%+v error=%v, want DELETED with no pending operation", got, err)
	}
	if runtime.cloneCalls != 2 || runtime.startCalls != 3 || runtime.stopCalls != 3 || runtime.verifyCalls != 8 || runtime.deleteCalls != 2 {
		t.Fatalf("runtime calls clone/start/stop/verify/delete=%d/%d/%d/%d/%d", runtime.cloneCalls, runtime.startCalls, runtime.stopCalls, runtime.verifyCalls, runtime.deleteCalls)
	}
}

func TestUnknownCreateIsReconciledByObservationWithoutRetry(t *testing.T) {
	runtime := &fakeRuntime{available: true, cloneErr: ErrOutcomeUnknown, state: RuntimeMissing}
	db, manager := setupManager(t, runtime)
	ctx := context.Background()
	_, err := manager.Create(ctx, CreateRequest{ID: "binding-a", SessionID: "session-a", EpochID: "epoch-a", Generation: "gen-1"})
	if !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("Create() error = %v, want unknown outcome", err)
	}
	binding, err := db.GetSessionRuntimeBinding(ctx, "session-a", "epoch-a")
	if err != nil || binding.State != "UNKNOWN" || binding.PendingOperation != "create" {
		t.Fatalf("persisted ambiguous create=%+v error=%v", binding, err)
	}
	if _, err := manager.Reconcile(ctx, "session-a", "epoch-a"); !errors.Is(err, ErrReconciliationNeeded) {
		t.Fatalf("Reconcile() error = %v, want fail-closed result for missing ambiguous create", err)
	}
	if runtime.cloneCalls != 1 {
		t.Fatalf("clone was retried %d times, want exactly one request", runtime.cloneCalls)
	}
}

func TestUnknownCreateWithMatchingStoppedLXCReconcilesWithoutCloningAgain(t *testing.T) {
	runtime := &fakeRuntime{available: true, cloneErr: ErrOutcomeUnknown, state: RuntimeStopped}
	_, manager := setupManager(t, runtime)
	ctx := context.Background()
	_, err := manager.Create(ctx, CreateRequest{ID: "binding-a", SessionID: "session-a", EpochID: "epoch-a", Generation: "gen-1"})
	if !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("Create() error = %v, want unknown outcome", err)
	}
	binding, err := manager.Reconcile(ctx, "session-a", "epoch-a")
	if err != nil || binding.State != "STOPPED" {
		t.Fatalf("Reconcile() binding=%+v error=%v, want verified STOPPED runtime", binding, err)
	}
	if runtime.cloneCalls != 1 || runtime.verifyCalls != 1 {
		t.Fatalf("clone/identity calls = %d/%d, want 1/1", runtime.cloneCalls, runtime.verifyCalls)
	}
}

func TestOccupiedSessionVMIDFailsBeforeClone(t *testing.T) {
	runtime := &fakeRuntime{available: false, state: RuntimeRunning}
	db, manager := setupManager(t, runtime)
	_, err := manager.Create(context.Background(), CreateRequest{ID: "binding-a", SessionID: "session-a", EpochID: "epoch-a", Generation: "gen-1"})
	if !errors.Is(err, ErrVMIDOccupied) {
		t.Fatalf("Create() error = %v, want VMID collision", err)
	}
	if runtime.cloneCalls != 0 {
		t.Fatalf("Clone() calls = %d, want none for occupied VMID", runtime.cloneCalls)
	}
	binding, getErr := db.GetSessionRuntimeBinding(context.Background(), "session-a", "epoch-a")
	if getErr != nil || binding.State != "FAILED" {
		t.Fatalf("persisted occupied binding=%+v error=%v", binding, getErr)
	}
}

func TestUnknownInventoryPreflightPersistsCreateIntent(t *testing.T) {
	runtime := &fakeRuntime{availableErr: errors.New("inventory unavailable")}
	db, manager := setupManager(t, runtime)
	_, err := manager.Create(context.Background(), CreateRequest{ID: "binding-a", SessionID: "session-a", EpochID: "epoch-a", Generation: "gen-1"})
	if !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("Create() error = %v, want unknown preflight outcome", err)
	}
	binding, getErr := db.GetSessionRuntimeBinding(context.Background(), "session-a", "epoch-a")
	if getErr != nil || binding.State != "UNKNOWN" || binding.PendingOperation != "create" {
		t.Fatalf("persisted preflight result=%+v error=%v, want UNKNOWN/create", binding, getErr)
	}
	if runtime.cloneCalls != 0 {
		t.Fatalf("Clone() calls = %d, want none after failed inventory preflight", runtime.cloneCalls)
	}
}

func TestStopRefusesRuntimeWhenOwnershipIdentityDoesNotMatch(t *testing.T) {
	runtime := &fakeRuntime{available: true, state: RuntimeRunning}
	db, manager := setupManager(t, runtime)
	ctx := context.Background()
	if _, err := manager.Create(ctx, CreateRequest{ID: "binding-a", SessionID: "session-a", EpochID: "epoch-a", Generation: "gen-1"}); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	runtime.verifyErr = errors.New("foreign owner")
	if err := manager.Stop(ctx, "session-a", "epoch-a"); !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("Stop() error = %v, want fail-closed UNKNOWN", err)
	}
	if runtime.stopCalls != 0 {
		t.Fatalf("Stop() external calls = %d, want zero when identity check fails", runtime.stopCalls)
	}
	binding, err := db.GetSessionRuntimeBinding(ctx, "session-a", "epoch-a")
	if err != nil || binding.State != "UNKNOWN" || binding.PendingOperation != "stop" {
		t.Fatalf("persisted identity mismatch binding=%+v error=%v", binding, err)
	}
}

func TestAmbiguousDeleteReconcilesMissingRuntimeWithoutRetry(t *testing.T) {
	runtime := &fakeRuntime{available: true, state: RuntimeMissing}
	db, manager := setupManager(t, runtime)
	ctx := context.Background()
	if _, err := manager.Create(ctx, CreateRequest{ID: "binding-a", SessionID: "session-a", EpochID: "epoch-a", Generation: "gen-1"}); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if err := manager.Stop(ctx, "session-a", "epoch-a"); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	runtime.deleteErr = ErrOutcomeUnknown
	if err := manager.Delete(ctx, "session-a", "epoch-a"); !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("Delete() error = %v, want unknown outcome", err)
	}
	runtime.state = RuntimeMissing
	binding, err := manager.Reconcile(ctx, "session-a", "epoch-a")
	if err != nil || binding.State != "DELETED" {
		t.Fatalf("Reconcile() binding=%+v error=%v, want DELETED", binding, err)
	}
	if runtime.deleteCalls != 1 {
		t.Fatalf("Delete() external calls = %d, want one without retry", runtime.deleteCalls)
	}
	history, err := db.ListSessionRuntimeBindingHistory(ctx, "session-a", "epoch-a")
	if err != nil || len(history) != 1 || history[0].VMID != binding.VMID {
		t.Fatalf("deleted generation history=%+v error=%v", history, err)
	}
}

func TestNewManagerRejectsNonSessionTemplateOrMissingSSDStorage(t *testing.T) {
	runtime := &fakeRuntime{}
	db, _ := setupManager(t, runtime)
	if _, err := NewManager(db, runtime, Config{TemplateVMID: 3004, SystemStorage: "local"}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("worker template accepted: %v", err)
	}
	if _, err := NewManager(db, runtime, Config{TemplateVMID: 3900}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("missing system storage accepted: %v", err)
	}
	if _, err := NewManager(db, runtime, Config{TemplateVMID: 3900, SystemStorage: "pool"}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("HDD pool accepted for Session system disk: %v", err)
	}
	if _, err := NewManager(db, runtime, Config{TemplateVMID: 3900, SystemStorage: "local", WorkspaceStorage: "local", WorkspaceSizeGiB: 32}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("SSD system storage accepted for Session workspace disk: %v", err)
	}
}
