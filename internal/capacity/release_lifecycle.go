package capacity

import (
	"context"
	"errors"
	"fmt"
)

var ErrReleaseInvalid = errors.New("worker release request is invalid")

// WorkerReleaseOperations is the narrow, injectable boundary for removing a
// dynamic worker. Implementations must make each operation idempotent and
// return ErrUnknown when its external outcome cannot be established.
type WorkerReleaseOperations interface {
	Cordon(context.Context, Node) error
	Drain(context.Context, Node) error
	RemoveNode(context.Context, Node) error
	VerifyNodeRemoved(context.Context, Node) error
	Stop(context.Context, Node) error
	VerifyStopped(context.Context, Node) error
	VerifyIdentity(context.Context, Node) error
	Destroy(context.Context, Node) error
}

// ReleaseWorker enforces the destructive release order. It deliberately does
// not retry or infer progress after an unknown external outcome; callers must
// persist progress and reconcile before invoking a later step.
func ReleaseWorker(ctx context.Context, node Node, operations WorkerReleaseOperations) error {
	if operations == nil || node.VMID < 3000 || node.VMID > 3999 || node.Generation == "" || node.TaskID == "" || node.KubeNode == "" || node.State == NodeUnknown {
		return ErrReleaseInvalid
	}
	steps := []struct {
		name string
		call func(context.Context, Node) error
	}{
		{"cordon", operations.Cordon},
		{"drain", operations.Drain},
		{"remove Kubernetes node", operations.RemoveNode},
		{"verify Kubernetes node removal", operations.VerifyNodeRemoved},
		{"verify Proxmox identity", operations.VerifyIdentity},
		{"stop", operations.Stop},
		{"verify stopped", operations.VerifyStopped},
		{"destroy", operations.Destroy},
	}
	for _, step := range steps {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := step.call(ctx, node); err != nil {
			return fmt.Errorf("release step %s: %w", step.name, err)
		}
	}
	return nil
}
