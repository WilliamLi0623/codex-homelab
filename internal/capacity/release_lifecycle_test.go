package capacity

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type releaseOps struct {
	order []string
	fail  string
	err   error
}

func (o *releaseOps) call(name string) error {
	o.order = append(o.order, name)
	if name == o.fail {
		return o.err
	}
	return nil
}
func (o *releaseOps) Cordon(context.Context, Node) error     { return o.call("cordon") }
func (o *releaseOps) Drain(context.Context, Node) error      { return o.call("drain") }
func (o *releaseOps) RemoveNode(context.Context, Node) error { return o.call("remove-node") }
func (o *releaseOps) VerifyNodeRemoved(context.Context, Node) error {
	return o.call("verify-node-removed")
}
func (o *releaseOps) Stop(context.Context, Node) error           { return o.call("stop") }
func (o *releaseOps) VerifyStopped(context.Context, Node) error  { return o.call("verify-stopped") }
func (o *releaseOps) VerifyIdentity(context.Context, Node) error { return o.call("verify-identity") }
func (o *releaseOps) Destroy(context.Context, Node) error        { return o.call("destroy") }

func validReleaseNode() Node {
	return Node{VMID: 3010, Generation: "gen-1", TaskID: "task-1", AttemptID: "attempt-1", KubeNode: "codex-3010-gen-1", State: NodeStopped}
}

func TestReleaseWorkerUsesMandatoryOrder(t *testing.T) {
	operations := &releaseOps{}
	if err := ReleaseWorker(context.Background(), validReleaseNode(), operations); err != nil {
		t.Fatalf("ReleaseWorker() error = %v", err)
	}
	want := []string{"cordon", "drain", "remove-node", "verify-node-removed", "verify-identity", "stop", "verify-stopped", "destroy"}
	if !reflect.DeepEqual(operations.order, want) {
		t.Fatalf("order = %v, want %v", operations.order, want)
	}
}

func TestReleaseWorkerStopsAtEveryFailureAndUnknownIsNotRetried(t *testing.T) {
	steps := []string{"cordon", "drain", "remove-node", "verify-node-removed", "verify-identity", "stop", "verify-stopped", "destroy"}
	for _, failed := range steps {
		t.Run(failed, func(t *testing.T) {
			operations := &releaseOps{fail: failed, err: ErrUnknown}
			if err := ReleaseWorker(context.Background(), validReleaseNode(), operations); !errors.Is(err, ErrUnknown) {
				t.Fatalf("error = %v, want ErrUnknown", err)
			}
			if operations.order[len(operations.order)-1] != failed {
				t.Fatalf("last operation = %v, want %s", operations.order, failed)
			}
		})
	}
}

func TestReleaseWorkerRejectsUnsafeInputsBeforeMutation(t *testing.T) {
	for name, node := range map[string]Node{
		"protected range":  {VMID: 220, Generation: "gen", TaskID: "task", KubeNode: "node", State: NodeStopped},
		"unknown":          {VMID: 3010, Generation: "gen", TaskID: "task", KubeNode: "node", State: NodeUnknown},
		"missing identity": {VMID: 3010, Generation: "", TaskID: "task", KubeNode: "node", State: NodeStopped},
	} {
		t.Run(name, func(t *testing.T) {
			operations := &releaseOps{}
			if err := ReleaseWorker(context.Background(), node, operations); !errors.Is(err, ErrReleaseInvalid) {
				t.Fatalf("error = %v, want ErrReleaseInvalid", err)
			}
			if len(operations.order) != 0 {
				t.Fatalf("mutations = %v, want none", operations.order)
			}
		})
	}
}

func TestReleaseWorkerHonorsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	operations := &releaseOps{}
	if err := ReleaseWorker(ctx, validReleaseNode(), operations); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if len(operations.order) != 0 {
		t.Fatalf("mutations = %v, want none", operations.order)
	}
}

func TestReleaseWorkerFromStepResumesAfterVerifiedCheckpoint(t *testing.T) {
	operations := &releaseOps{}
	if err := ReleaseWorkerFromStep(context.Background(), validReleaseNode(), operations, ReleaseStepVerifyIdentity, nil); err != nil {
		t.Fatalf("ReleaseWorkerFromStep() error = %v", err)
	}
	want := []string{"verify-identity", "stop", "verify-stopped", "destroy"}
	if !reflect.DeepEqual(operations.order, want) {
		t.Fatalf("resumed order = %v, want %v", operations.order, want)
	}
}

func TestReleaseWorkerCheckpointRecordsSuccessAndUnknown(t *testing.T) {
	operations := &releaseOps{fail: "stop", err: ErrUnknown}
	var checkpoints []string
	err := ReleaseWorkerFromStep(context.Background(), validReleaseNode(), operations, ReleaseStepCordon, func(_ context.Context, _ Node, step ReleaseStep, stepErr error) error {
		if stepErr == nil {
			checkpoints = append(checkpoints, string(step)+":COMPLETED")
		} else {
			checkpoints = append(checkpoints, string(step)+":UNKNOWN")
		}
		return nil
	})
	if !errors.Is(err, ErrUnknown) {
		t.Fatalf("error = %v, want ErrUnknown", err)
	}
	want := []string{"CORDON:COMPLETED", "DRAIN:COMPLETED", "REMOVE_NODE:COMPLETED", "VERIFY_NODE_REMOVED:COMPLETED", "VERIFY_IDENTITY:COMPLETED", "STOP:UNKNOWN"}
	if !reflect.DeepEqual(checkpoints, want) {
		t.Fatalf("checkpoints = %v, want %v", checkpoints, want)
	}
}
