package capacity

import (
	"context"
	"errors"
	"testing"
)

type fakeRuntime struct {
	createErr    error
	observeNode  Node
	startCalls   []int
	joinCalls    []int
	stopCalls    []int
	destroyCalls []int
}

func (f *fakeRuntime) Create(context.Context, CreateRequest) (Node, error) {
	if f.createErr != nil {
		return Node{}, f.createErr
	}
	return Node{VMID: 3001, Generation: "gen-1", State: NodeCreating}, nil
}

func (f *fakeRuntime) Observe(context.Context, int) (Node, error) { return f.observeNode, nil }
func (f *fakeRuntime) Start(_ context.Context, vmid int) error {
	f.startCalls = append(f.startCalls, vmid)
	return nil
}
func (f *fakeRuntime) Join(_ context.Context, vmid int) error {
	f.joinCalls = append(f.joinCalls, vmid)
	return nil
}
func (f *fakeRuntime) Stop(_ context.Context, vmid int) error {
	f.stopCalls = append(f.stopCalls, vmid)
	return nil
}
func (f *fakeRuntime) Destroy(_ context.Context, vmid int) error {
	f.destroyCalls = append(f.destroyCalls, vmid)
	return nil
}

func newTestManager(runtime Runtime) *Manager {
	return NewManager(runtime, VMIDRange{Min: 3000, Max: 3999})
}

func TestManagerRejectsProtectedVMIDBeforeRuntimeMutation(t *testing.T) {
	fake := &fakeRuntime{}
	manager := newTestManager(fake)

	err := manager.Start(context.Background(), Node{VMID: 220, State: NodeStopped})
	if !errors.Is(err, ErrVMIDOutsideRange) {
		t.Fatalf("Start(protected VMID) error = %v, want %v", err, ErrVMIDOutsideRange)
	}
	if len(fake.startCalls) != 0 {
		t.Fatalf("runtime Start calls = %v, want none", fake.startCalls)
	}
}

func TestManagerRejectsDestroyUntilDrainAndNodeRemovalAreRecorded(t *testing.T) {
	fake := &fakeRuntime{}
	manager := newTestManager(fake)
	node := Node{VMID: 3001, State: NodeStopped}

	err := manager.Destroy(context.Background(), node)
	if !errors.Is(err, ErrLifecycleNotReady) {
		t.Fatalf("Destroy(unverified node) error = %v, want %v", err, ErrLifecycleNotReady)
	}
	if len(fake.destroyCalls) != 0 {
		t.Fatalf("runtime Destroy calls = %v, want none", fake.destroyCalls)
	}
}

func TestManagerRecordsUnknownAndDoesNotReplayMutation(t *testing.T) {
	fake := &fakeRuntime{createErr: ErrUnknown}
	manager := newTestManager(fake)

	node, err := manager.Create(context.Background(), CreateRequest{VMID: 3001, Generation: "gen-1"})
	if !errors.Is(err, ErrUnknown) {
		t.Fatalf("Create(ambiguous) error = %v, want %v", err, ErrUnknown)
	}
	if node.State != NodeUnknown {
		t.Fatalf("ambiguous node state = %q, want %q", node.State, NodeUnknown)
	}

	if err := manager.Start(context.Background(), node); !errors.Is(err, ErrUnknownUnresolved) {
		t.Fatalf("Start(unresolved UNKNOWN) error = %v, want %v", err, ErrUnknownUnresolved)
	}
	if len(fake.startCalls) != 0 {
		t.Fatalf("runtime Start calls = %v, want none", fake.startCalls)
	}
}

func TestManagerReconcileResolvesUnknownFromObservedState(t *testing.T) {
	fake := &fakeRuntime{observeNode: Node{VMID: 3001, Generation: "gen-1", State: NodeRunning}}
	manager := newTestManager(fake)

	node, err := manager.Reconcile(context.Background(), Node{VMID: 3001, State: NodeUnknown})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if node.State != NodeRunning {
		t.Fatalf("reconciled state = %q, want %q", node.State, NodeRunning)
	}
}
