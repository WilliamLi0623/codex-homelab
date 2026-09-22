package orchestrator

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/WilliamLi0623/codex-homelab/internal/capacity"
)

type releaseK3sFake struct{ calls []string }

func (f *releaseK3sFake) call(name string) error                   { f.calls = append(f.calls, name); return nil }
func (f *releaseK3sFake) Cordon(context.Context, string) error     { return f.call("cordon") }
func (f *releaseK3sFake) Drain(context.Context, string) error      { return f.call("drain") }
func (f *releaseK3sFake) RemoveNode(context.Context, string) error { return f.call("remove-node") }
func (f *releaseK3sFake) VerifyNodeRemoved(context.Context, string) error {
	return f.call("verify-node-removed")
}

type releaseProxmoxFake struct {
	calls []string
	state capacity.NodeState
}

func (f *releaseProxmoxFake) call(name string) error { f.calls = append(f.calls, name); return nil }
func (f *releaseProxmoxFake) VerifyIdentity(context.Context, capacity.Node) error {
	return f.call("verify-identity")
}
func (f *releaseProxmoxFake) Observe(context.Context, int) (capacity.Node, error) {
	f.calls = append(f.calls, "observe")
	return capacity.Node{VMID: 3010, State: f.state}, nil
}
func (f *releaseProxmoxFake) Stop(context.Context, int) error    { return f.call("stop") }
func (f *releaseProxmoxFake) Destroy(context.Context, int) error { return f.call("destroy") }

func TestReleaseOperationsComposesK3sAndProxmoxBoundaries(t *testing.T) {
	k3s := &releaseK3sFake{}
	proxmox := &releaseProxmoxFake{state: capacity.NodeStopped}
	operations := NewReleaseOperations(k3s, proxmox)
	node := capacity.Node{VMID: 3010, Generation: "gen-1", TaskID: "task-1", KubeNode: "codex-node", State: capacity.NodeStopped}
	if err := capacity.ReleaseWorker(context.Background(), node, operations); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(k3s.calls, []string{"cordon", "drain", "remove-node", "verify-node-removed"}) {
		t.Fatalf("K3s calls = %v", k3s.calls)
	}
	if !reflect.DeepEqual(proxmox.calls, []string{"verify-identity", "stop", "verify-identity", "stop", "observe", "destroy"}) {
		t.Fatalf("Proxmox calls = %v", proxmox.calls)
	}
}

func TestReleaseOperationsQuiescesBeforeNodeDeletion(t *testing.T) {
	k3s := &releaseK3sFake{}
	proxmox := &releaseProxmoxFake{state: capacity.NodeRunning}
	operations := NewReleaseOperations(k3s, proxmox)
	node := capacity.Node{VMID: 3010, Generation: "gen-1", TaskID: "task-1", KubeNode: "codex-node", State: capacity.NodeStopped}
	if err := operations.RemoveNode(context.Background(), node); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(proxmox.calls, []string{"verify-identity", "stop"}) || !reflect.DeepEqual(k3s.calls, []string{"remove-node"}) {
		t.Fatalf("proxmox=%v k3s=%v; want identity, stop, then remove-node", proxmox.calls, k3s.calls)
	}
}

func TestReleaseOperationsFailsClosedWhenEitherBoundaryMissing(t *testing.T) {
	if err := capacity.ReleaseWorker(context.Background(), capacity.Node{VMID: 3010, Generation: "gen", TaskID: "task", KubeNode: "node", State: capacity.NodeStopped}, NewReleaseOperations(nil, nil)); !errors.Is(err, ErrReleaseOperationsUnavailable) {
		t.Fatalf("error = %v", err)
	}
}
