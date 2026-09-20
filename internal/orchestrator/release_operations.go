package orchestrator

import (
	"context"
	"errors"
	"fmt"

	"github.com/WilliamLi0623/codex-homelab/internal/capacity"
)

var ErrReleaseOperationsUnavailable = errors.New("release operations are not configured")

type K3sNodeLifecycle interface {
	Cordon(context.Context, string) error
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
	k3s     K3sNodeLifecycle
	proxmox ProxmoxNodeLifecycle
}

func NewReleaseOperations(k3s K3sNodeLifecycle, proxmox ProxmoxNodeLifecycle) *ReleaseOperations {
	return &ReleaseOperations{k3s: k3s, proxmox: proxmox}
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
	return o.k3s.Drain(ctx, node.KubeNode)
}
func (o *ReleaseOperations) RemoveNode(ctx context.Context, node capacity.Node) error {
	if err := o.validate(); err != nil {
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
	observed, err := o.proxmox.Observe(ctx, node.VMID)
	if err != nil {
		return err
	}
	if observed.VMID != node.VMID || observed.State != capacity.NodeStopped {
		return fmt.Errorf("worker VMID %d is not stopped", node.VMID)
	}
	return nil
}
func (o *ReleaseOperations) Destroy(ctx context.Context, node capacity.Node) error {
	if err := o.validate(); err != nil {
		return err
	}
	return o.proxmox.Destroy(ctx, node.VMID)
}
