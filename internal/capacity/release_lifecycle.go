package capacity

import (
	"context"
	"errors"
	"fmt"
)

var ErrReleaseInvalid = errors.New("worker release request is invalid")

type ReleaseStep string

const (
	ReleaseStepCordon            ReleaseStep = "CORDON"
	ReleaseStepDrain             ReleaseStep = "DRAIN"
	ReleaseStepRemoveNode        ReleaseStep = "REMOVE_NODE"
	ReleaseStepVerifyNodeRemoved ReleaseStep = "VERIFY_NODE_REMOVED"
	ReleaseStepVerifyIdentity    ReleaseStep = "VERIFY_IDENTITY"
	ReleaseStepStop              ReleaseStep = "STOP"
	ReleaseStepVerifyStopped     ReleaseStep = "VERIFY_STOPPED"
	ReleaseStepDestroy           ReleaseStep = "DESTROY"
)

type ReleaseCheckpoint func(context.Context, Node, ReleaseStep, error) error

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
	return ReleaseWorkerFromStep(ctx, node, operations, ReleaseStepCordon, nil)
}

// ReleaseWorkerFromStep resumes at start after a previously durable completed
// step. An UNKNOWN step must be explicitly reconciled by the caller before it
// is passed here; this function never infers external progress.
func ReleaseWorkerFromStep(ctx context.Context, node Node, operations WorkerReleaseOperations, start ReleaseStep, checkpoint ReleaseCheckpoint) error {
	if operations == nil || node.VMID < 3000 || node.VMID > 3899 || node.Generation == "" || node.TaskID == "" || node.AttemptID == "" || node.KubeNode == "" || node.State == NodeUnknown {
		return ErrReleaseInvalid
	}
	steps := []struct {
		step ReleaseStep
		call func(context.Context, Node) error
	}{
		{ReleaseStepCordon, operations.Cordon},
		{ReleaseStepDrain, operations.Drain},
		{ReleaseStepRemoveNode, operations.RemoveNode},
		{ReleaseStepVerifyNodeRemoved, operations.VerifyNodeRemoved},
		{ReleaseStepVerifyIdentity, operations.VerifyIdentity},
		{ReleaseStepStop, operations.Stop},
		{ReleaseStepVerifyStopped, operations.VerifyStopped},
		{ReleaseStepDestroy, operations.Destroy},
	}
	startIndex := -1
	for index, step := range steps {
		if step.step == start {
			startIndex = index
			break
		}
	}
	if startIndex < 0 {
		return ErrReleaseInvalid
	}
	for _, step := range steps[startIndex:] {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := step.call(ctx, node); err != nil {
			if checkpoint != nil {
				if checkpointErr := checkpoint(ctx, node, step.step, err); checkpointErr != nil {
					return fmt.Errorf("release step %s failed and checkpoint failed: %w", step.step, checkpointErr)
				}
			}
			return fmt.Errorf("release step %s: %w", step.step, err)
		}
		if checkpoint != nil {
			if err := checkpoint(ctx, node, step.step, nil); err != nil {
				return fmt.Errorf("release step %s checkpoint: %w", step.step, err)
			}
		}
	}
	return nil
}
