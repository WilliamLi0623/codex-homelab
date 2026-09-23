package orchestrator

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/WilliamLi0623/codex-homelab/internal/capacity"
	"github.com/WilliamLi0623/codex-homelab/internal/store"
)

type adapterRuntime struct {
	createCalls  int
	startCalls   int
	createErr    error
	createErrs   []error
	occupied     map[int]bool
	destroyCalls int
	lastCreate   capacity.CreateRequest
}

type adapterReleaseOps struct {
	order []string
	fail  string
	err   error
}

func (o *adapterReleaseOps) mark(step string) error {
	o.order = append(o.order, step)
	if step == o.fail {
		return o.err
	}
	return nil
}
func (o *adapterReleaseOps) Cordon(context.Context, capacity.Node) error {
	return o.mark("cordon")
}
func (o *adapterReleaseOps) Drain(context.Context, capacity.Node) error { return o.mark("drain") }
func (o *adapterReleaseOps) RemoveNode(context.Context, capacity.Node) error {
	return o.mark("remove-node")
}
func (o *adapterReleaseOps) VerifyNodeRemoved(context.Context, capacity.Node) error {
	return o.mark("verify-node-removed")
}
func (o *adapterReleaseOps) VerifyIdentity(context.Context, capacity.Node) error {
	return o.mark("verify-identity")
}
func (o *adapterReleaseOps) Stop(context.Context, capacity.Node) error { return o.mark("stop") }
func (o *adapterReleaseOps) VerifyStopped(context.Context, capacity.Node) error {
	return o.mark("verify-stopped")
}
func (o *adapterReleaseOps) Destroy(context.Context, capacity.Node) error {
	return o.mark("destroy")
}

var _ Capacity = (*CapacityAdapter)(nil)

func (r *adapterRuntime) Create(_ context.Context, request capacity.CreateRequest) (capacity.Node, error) {
	r.createCalls++
	r.lastCreate = request
	if len(r.createErrs) > 0 {
		err := r.createErrs[0]
		r.createErrs = r.createErrs[1:]
		if err != nil {
			return capacity.Node{}, err
		}
	}
	if r.createErr != nil {
		return capacity.Node{}, r.createErr
	}
	return capacity.Node{VMID: request.VMID, Generation: request.Generation, TaskID: request.TaskID, State: capacity.NodeCreating}, nil
}
func (r *adapterRuntime) Observe(_ context.Context, vmid int) (capacity.Node, error) {
	if r.occupied != nil && r.occupied[vmid] {
		return capacity.Node{VMID: vmid, State: capacity.NodeStopped}, nil
	}
	return capacity.Node{}, capacity.ErrNotFound
}
func (r *adapterRuntime) Start(context.Context, int) error   { r.startCalls++; return nil }
func (r *adapterRuntime) Join(context.Context, int) error    { return nil }
func (r *adapterRuntime) Stop(context.Context, int) error    { return nil }
func (r *adapterRuntime) Destroy(context.Context, int) error { r.destroyCalls++; return nil }

func TestCapacityAdapterRecordsUnknownAndDoesNotReplayCreateOrRelease(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "capacity.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	runtime := &adapterRuntime{createErr: capacity.ErrUnknown}
	adapter := NewCapacityAdapter(db, capacity.NewManager(runtime, capacity.VMIDRange{Min: 3000, Max: 3899}), 3900)
	req := CapacityClaimRequest{TaskID: "task", AttemptID: "attempt", Generation: "caller-gen", Priority: 10}
	claim, err := adapter.Claim(context.Background(), req)
	if !errors.Is(err, capacity.ErrUnknown) || claim.State != string(capacity.NodeUnknown) {
		t.Fatalf("first claim = %+v, %v", claim, err)
	}
	if _, err := adapter.Claim(context.Background(), req); !errors.Is(err, ErrCapacityUnknown) {
		t.Fatalf("repeat UNKNOWN claim error = %v, want ErrCapacityUnknown", err)
	}
	if runtime.createCalls != 1 {
		t.Fatalf("create calls = %d, want 1", runtime.createCalls)
	}
	if err := adapter.Release(context.Background(), Claim{ID: claim.ID, VMID: claim.VMID}); !errors.Is(err, ErrCapacityReleaseUnsafe) {
		t.Fatalf("UNKNOWN release error = %v, want ErrCapacityReleaseUnsafe", err)
	}
	if runtime.destroyCalls != 0 {
		t.Fatalf("destroy calls = %d, want 0", runtime.destroyCalls)
	}
}

func TestCapacityAdapterSkipsExternallyOccupiedVMID(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "capacity.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	runtime := &adapterRuntime{occupied: map[int]bool{3000: true}}
	adapter := NewCapacityAdapter(db, capacity.NewManager(runtime, capacity.VMIDRange{Min: 3000, Max: 3899}), 3900)
	claim, err := adapter.Claim(context.Background(), CapacityClaimRequest{TaskID: "task", AttemptID: "attempt", Generation: "gen", Priority: 10})
	if err != nil {
		t.Fatal(err)
	}
	if claim.VMID != 3001 || runtime.lastCreate.VMID != 3001 {
		t.Fatalf("claim=%+v create=%+v, want externally occupied 3000 skipped", claim, runtime.lastCreate)
	}
}

func TestCapacityAdapterRetriesDeterministicVMIDCollision(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "capacity.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	runtime := &adapterRuntime{createErrs: []error{capacity.ErrVMIDOccupied, nil}}
	adapter := NewCapacityAdapter(db, capacity.NewManager(runtime, capacity.VMIDRange{Min: 3000, Max: 3899}), 3900)
	claim, err := adapter.Claim(context.Background(), CapacityClaimRequest{TaskID: "task", AttemptID: "attempt", Generation: "gen", Priority: 10})
	if err != nil {
		t.Fatal(err)
	}
	if claim.VMID != 3001 || runtime.createCalls != 2 {
		t.Fatalf("claim=%+v createCalls=%d, want collision retry on next VMID", claim, runtime.createCalls)
	}
}

func TestCapacityAdapterRemovesRejectedCreateClaimSoRetryCanProceed(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "capacity.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	runtime := &adapterRuntime{createErrs: []error{capacity.ErrRejected, nil}}
	adapter := NewCapacityAdapter(db, capacity.NewManager(runtime, capacity.VMIDRange{Min: 3000, Max: 3899}), 3900)
	req := CapacityClaimRequest{TaskID: "task", AttemptID: "attempt", Generation: "gen", Priority: 10}
	claim, err := adapter.Claim(context.Background(), req)
	if !errors.Is(err, capacity.ErrRejected) || claim.VMID != 3000 {
		t.Fatalf("first claim = %+v, %v; want rejected VMID 3000", claim, err)
	}
	if _, err := db.GetCapacityClaim(context.Background(), req.TaskID, req.AttemptID); !errors.Is(err, store.ErrCapacityClaimNotFound) {
		t.Fatalf("rejected claim remains in ledger: %v", err)
	}
	retried, err := adapter.Claim(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if retried.VMID != 3000 || runtime.createCalls != 2 {
		t.Fatalf("retry claim = %+v createCalls=%d, want VMID 3000 and two creates", retried, runtime.createCalls)
	}
}

func TestCapacityAdapterSameAttemptIsIdempotentAndDoesNotCreateTwice(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "capacity.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	runtime := &adapterRuntime{}
	adapter := NewCapacityAdapter(db, capacity.NewManager(runtime, capacity.VMIDRange{Min: 3000, Max: 3899}), 3900)
	req := CapacityClaimRequest{TaskID: "task", AttemptID: "attempt", Generation: "caller-gen", Priority: 10}
	first, err := adapter.Claim(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	second, err := adapter.Claim(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if first.VMID != second.VMID || first.Generation != second.Generation || runtime.createCalls != 1 {
		t.Fatalf("first=%+v second=%+v createCalls=%d", first, second, runtime.createCalls)
	}
	if runtime.startCalls != 1 {
		t.Fatalf("start calls = %d, want one start for the created worker", runtime.startCalls)
	}
}

func TestCapacityAdapterReleaseFailsClosedBeforeFullLifecycle(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "capacity.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	runtime := &adapterRuntime{}
	adapter := NewCapacityAdapter(db, capacity.NewManager(runtime, capacity.VMIDRange{Min: 3000, Max: 3899}), 3900)
	claim, err := adapter.Claim(context.Background(), CapacityClaimRequest{TaskID: "task", AttemptID: "attempt", Generation: "gen", Priority: 10})
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.Release(context.Background(), Claim{ID: claim.ID, VMID: claim.VMID}); !errors.Is(err, ErrCapacityReleaseUnsafe) {
		t.Fatalf("release error = %v, want ErrCapacityReleaseUnsafe", err)
	}
	if runtime.destroyCalls != 0 {
		t.Fatalf("destroy calls = %d, want 0", runtime.destroyCalls)
	}
}

func TestCapacityAdapterReleasePersistsOrderAndIsIdempotent(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "capacity.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	runtime := &adapterRuntime{}
	operations := &adapterReleaseOps{}
	adapter := NewCapacityAdapterWithConfig(db, capacity.NewManager(runtime, capacity.VMIDRange{Min: 3000, Max: 3899}), CapacityAdapterConfig{TemplateVMID: 3900, Priority: 1, Hostname: "codex-3010-gen-1", ReleaseOperations: operations})
	claim, err := adapter.Create(context.Background(), ClaimRequest{TaskID: "task", AttemptID: "attempt"})
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.Release(context.Background(), claim); err != nil {
		t.Fatalf("first Release() error = %v", err)
	}
	want := []string{"cordon", "drain", "remove-node", "verify-node-removed", "verify-identity", "stop", "verify-stopped", "destroy"}
	if len(operations.order) != len(want) {
		t.Fatalf("release order = %v", operations.order)
	}
	if err := adapter.Release(context.Background(), claim); err != nil {
		t.Fatalf("replay Release() error = %v", err)
	}
	if len(operations.order) != len(want) {
		t.Fatalf("replay mutated order = %v", operations.order)
	}
	progress, err := db.GetReleaseProgress(context.Background(), claim.TaskID, claim.AttemptID)
	if err != nil || progress.Step != store.ReleaseStepDone || progress.State != store.ReleaseStateCompleted {
		t.Fatalf("progress = (%+v, %v)", progress, err)
	}
	if _, err := db.GetCapacityClaim(context.Background(), claim.TaskID, claim.AttemptID); !errors.Is(err, store.ErrCapacityClaimNotFound) {
		t.Fatalf("released capacity claim error = %v, want not found", err)
	}
	reused, created, err := db.ClaimCapacity(context.Background(), store.CapacityClaimRequest{TaskID: "task-reuse", AttemptID: "attempt-reuse", Generation: "gen-reuse", Priority: 1})
	if err != nil || !created || reused.VMID != claim.VMID {
		t.Fatalf("reused claim = (%+v, %t, %v), want VMID %d", reused, created, err, claim.VMID)
	}
}

func TestCapacityAdapterRequiresExplicitReconcileAfterUnknown(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "capacity.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	runtime := &adapterRuntime{}
	operations := &adapterReleaseOps{fail: "stop", err: capacity.ErrUnknown}
	adapter := NewCapacityAdapterWithConfig(db, capacity.NewManager(runtime, capacity.VMIDRange{Min: 3000, Max: 3899}), CapacityAdapterConfig{TemplateVMID: 3900, Priority: 1, Hostname: "codex-3010-gen-1", ReleaseOperations: operations})
	claim, err := adapter.Create(context.Background(), ClaimRequest{TaskID: "task", AttemptID: "attempt"})
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.Release(context.Background(), claim); !errors.Is(err, capacity.ErrUnknown) {
		t.Fatalf("first release = %v", err)
	}
	progress, err := db.GetReleaseProgress(context.Background(), claim.TaskID, claim.AttemptID)
	if err != nil || !strings.Contains(progress.ErrorSummary, "external operation outcome is unknown") {
		t.Fatalf("unknown release summary = (%+v, %v)", progress, err)
	}
	if err := adapter.Release(context.Background(), claim); !errors.Is(err, ErrCapacityReleaseUnsafe) {
		t.Fatalf("unreconciled release = %v", err)
	}
	if err := adapter.ReconcileRelease(context.Background(), claim, "Proxmox and Kubernetes observations agree"); err != nil {
		t.Fatalf("reconcile = %v", err)
	}
	operations.fail = ""
	if err := adapter.Release(context.Background(), claim); err != nil {
		t.Fatalf("resumed release = %v", err)
	}
	if len(operations.order) != 9 {
		t.Fatalf("operations after resume = %v", operations.order)
	}
}

func TestCapacityAdapterImplementsCapacityWithDeterministicGeneration(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "capacity.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	runtime := &adapterRuntime{}
	adapter := NewCapacityAdapter(db, capacity.NewManager(runtime, capacity.VMIDRange{Min: 3000, Max: 3899}), 3900)
	claim, err := adapter.Create(context.Background(), ClaimRequest{TaskID: "task", AttemptID: "attempt"})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := db.GetCapacityClaim(context.Background(), "task", "attempt")
	if err != nil {
		t.Fatal(err)
	}
	if claim.ID != stored.ID || claim.VMID != stored.VMID || stored.Generation == "" {
		t.Fatalf("claim=%+v stored=%+v", claim, stored)
	}
	second, err := adapter.Create(context.Background(), ClaimRequest{TaskID: "task", AttemptID: "attempt"})
	if err != nil {
		t.Fatal(err)
	}
	if second != claim || runtime.createCalls != 1 {
		t.Fatalf("second=%+v first=%+v createCalls=%d", second, claim, runtime.createCalls)
	}
}

func TestCapacityAdapterReturnsUnknownWhenUnknownPersistenceFailsAndNeverRetries(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "capacity.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	persistErr := errors.New("injected UNKNOWN persistence failure")
	db.SetCapacityClaimStateUpdateHook(func(_ context.Context, _, _, state string) error {
		if state == store.CapacityUnknown {
			return persistErr
		}
		return nil
	})
	runtime := &adapterRuntime{createErr: capacity.ErrUnknown}
	adapter := NewCapacityAdapter(db, capacity.NewManager(runtime, capacity.VMIDRange{Min: 3000, Max: 3899}), 3900)
	req := ClaimRequest{TaskID: "task", AttemptID: "attempt"}
	_, err = adapter.Create(context.Background(), req)
	if !errors.Is(err, capacity.ErrUnknown) || !errors.Is(err, persistErr) || !errors.Is(err, ErrCapacityLedgerReconciliation) {
		t.Fatalf("first error = %v, want UNKNOWN, persistence, and reconciliation errors", err)
	}
	if _, err = adapter.Create(context.Background(), req); !errors.Is(err, ErrCapacityLedgerReconciliation) {
		t.Fatalf("retry error = %v, want reconciliation error for durable CREATING claim", err)
	}
	if runtime.createCalls != 1 {
		t.Fatalf("create calls = %d, want 1", runtime.createCalls)
	}
}

func TestCapacityAdapterClaimUsesConfiguredTemplateAndNilReceiverFailsClosed(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "capacity.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	runtime := &adapterRuntime{}
	adapter := NewCapacityAdapter(db, capacity.NewManager(runtime, capacity.VMIDRange{Min: 3000, Max: 3899}), 3900)
	if _, err := adapter.Claim(context.Background(), CapacityClaimRequest{TaskID: "task", AttemptID: "attempt", Generation: "gen", Priority: 1}); err != nil {
		t.Fatal(err)
	}
	if runtime.lastCreate.TemplateVMID != 3900 {
		t.Fatalf("template VMID = %d, want configured 3900", runtime.lastCreate.TemplateVMID)
	}
	var nilAdapter *CapacityAdapter
	if _, err := nilAdapter.Create(context.Background(), ClaimRequest{TaskID: "task", AttemptID: "attempt"}); err == nil {
		t.Fatal("nil Create() unexpectedly succeeded")
	}
	if _, err := nilAdapter.Claim(context.Background(), CapacityClaimRequest{TaskID: "task", AttemptID: "attempt", Generation: "gen", Priority: 1}); err == nil {
		t.Fatal("nil Claim() unexpectedly succeeded")
	}
}
