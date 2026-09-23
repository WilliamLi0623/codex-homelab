package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/WilliamLi0623/codex-homelab/internal/capacity"
)

var ErrReleaseOperationsUnavailable = errors.New("release operations are not configured")

type K3sNodeLifecycle interface {
	Cordon(context.Context, string) error
	CleanupExecution(context.Context, string, string) error
	Drain(context.Context, string) error
	RemoveNode(context.Context, string) error
	VerifyNodeRemoved(context.Context, string) error
}

type ProxmoxNodeLifecycle interface {
	VerifyIdentity(context.Context, capacity.Node) error
	Observe(context.Context, int) (capacity.Node, error)
	Stop(context.Context, int) error
	Destroy(context.Context, int) error
}

// ReleaseOperations composes the Kubernetes and Proxmox boundaries without
// allowing either subsystem to skip the mandatory release order.
type ReleaseOperations struct {
	k3s              K3sNodeLifecycle
	proxmox          ProxmoxNodeLifecycle
	stopTimeout      time.Duration
	stopPollInterval time.Duration
}

func NewReleaseOperations(k3s K3sNodeLifecycle, proxmox ProxmoxNodeLifecycle) *ReleaseOperations {
	return &ReleaseOperations{k3s: k3s, proxmox: proxmox, stopTimeout: 30 * time.Second, stopPollInterval: 250 * time.Millisecond}
}

func (o *ReleaseOperations) validate() error {
	if o == nil || o.k3s == nil || o.proxmox == nil {
		return ErrReleaseOperationsUnavailable
	}
	return nil
}

func (o *ReleaseOperations) Cordon(ctx context.Context, node capacity.Node) error {
	if err := o.validate(); err != nil {
		return err
	}
	return o.k3s.Cordon(ctx, node.KubeNode)
}
func (o *ReleaseOperations) Drain(ctx context.Context, node capacity.Node) error {
	if err := o.validate(); err != nil {
		return err
	}
	if node.TaskID == "" || node.AttemptID == "" {
		return capacity.ErrReleaseInvalid
	}
	if err := o.k3s.CleanupExecution(ctx, node.TaskID, node.AttemptID); err != nil {
		return err
	}
	return o.k3s.Drain(ctx, node.KubeNode)
}
func (o *ReleaseOperations) RemoveNode(ctx context.Context, node capacity.Node) error {
	if err := o.validate(); err != nil {
		return err
	}
	// A live k3s agent can immediately re-register the Node after the API
	// accepts its deletion. Verify the exact Proxmox identity and stop the
	// worker before deleting the Kubernetes Node. Stop is idempotent, and the
	// later STOP checkpoint remains as a durable compatibility/reverification
	// step for existing release progress.
	if err := o.proxmox.VerifyIdentity(ctx, node); err != nil {
		return err
	}
	if err := o.proxmox.Stop(ctx, node.VMID); err != nil {
		return err
	}
	return o.k3s.RemoveNode(ctx, node.KubeNode)
}
func (o *ReleaseOperations) VerifyNodeRemoved(ctx context.Context, node capacity.Node) error {
	if err := o.validate(); err != nil {
		return err
	}
	return o.k3s.VerifyNodeRemoved(ctx, node.KubeNode)
}
func (o *ReleaseOperations) VerifyIdentity(ctx context.Context, node capacity.Node) error {
	if err := o.validate(); err != nil {
		return err
	}
	return o.proxmox.VerifyIdentity(ctx, node)
}
func (o *ReleaseOperations) Stop(ctx context.Context, node capacity.Node) error {
	if err := o.validate(); err != nil {
		return err
	}
	return o.proxmox.Stop(ctx, node.VMID)
}
func (o *ReleaseOperations) VerifyStopped(ctx context.Context, node capacity.Node) error {
	if err := o.validate(); err != nil {
		return err
	}
	timeout := o.stopTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	interval := o.stopPollInterval
	if interval <= 0 {
		interval = 250 * time.Millisecond
	}
	pollCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		observed, err := o.proxmox.Observe(pollCtx, node.VMID)
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
				return fmt.Errorf("worker VMID %d did not reach stopped state before timeout", node.VMID)
			}
			return err
		}
		if observed.VMID != node.VMID {
			return fmt.Errorf("Proxmox returned VMID %d while verifying worker VMID %d", observed.VMID, node.VMID)
		}
		if observed.State == capacity.NodeStopped {
			return nil
		}
		select {
		case <-pollCtx.Done():
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("worker VMID %d did not reach stopped state (last state %s)", node.VMID, observed.State)
		case <-ticker.C:
		}
	}
}
func (o *ReleaseOperations) Destroy(ctx context.Context, node capacity.Node) error {
	if err := o.validate(); err != nil {
		return err
	}
	return o.proxmox.Destroy(ctx, node.VMID)
}
