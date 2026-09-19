package capacity

import (
	"context"
	"errors"
	"fmt"
)

var (
	ErrVMIDOutsideRange  = errors.New("vmid is outside the dynamic range")
	ErrLifecycleNotReady = errors.New("worker lifecycle is not ready for this operation")
	ErrUnknown           = errors.New("external operation outcome is unknown")
	ErrUnknownUnresolved = errors.New("unknown external outcome must be reconciled before mutation")
)

type NodeState string

const (
	NodeCreating NodeState = "CREATING"
	NodeRunning  NodeState = "RUNNING"
	NodeStopped  NodeState = "STOPPED"
	NodeUnknown  NodeState = "UNKNOWN"
)

type VMIDRange struct {
	Min int
	Max int
}

func (r VMIDRange) Contains(vmid int) bool {
	return vmid >= r.Min && vmid <= r.Max
}

type Node struct {
	VMID        int
	Generation  string
	TaskID      string
	KubeNode    string
	State       NodeState
	Drained     bool
	NodeRemoved bool
}

type CreateRequest struct {
	VMID         int
	Generation   string
	TaskID       string
	TemplateVMID int
	Hostname     string
	Storage      string
	Bridge       string
	Cores        int
	MemoryMiB    int
	DiskGiB      int
}

type Runtime interface {
	Create(context.Context, CreateRequest) (Node, error)
	Observe(context.Context, int) (Node, error)
	Start(context.Context, int) error
	Join(context.Context, int) error
	Stop(context.Context, int) error
	Destroy(context.Context, int) error
}

type Manager struct {
	runtime Runtime
	range_  VMIDRange
}

func NewManager(runtime Runtime, vmidRange VMIDRange) *Manager {
	return &Manager{runtime: runtime, range_: vmidRange}
}

func (m *Manager) validate(vmid int) error {
	if !m.range_.Contains(vmid) {
		return fmt.Errorf("%w: %d not in %d-%d", ErrVMIDOutsideRange, vmid, m.range_.Min, m.range_.Max)
	}
	return nil
}

func (m *Manager) Create(ctx context.Context, request CreateRequest) (Node, error) {
	if err := m.validate(request.VMID); err != nil {
		return Node{}, err
	}
	node, err := m.runtime.Create(ctx, request)
	if errors.Is(err, ErrUnknown) {
		return Node{VMID: request.VMID, Generation: request.Generation, TaskID: request.TaskID, State: NodeUnknown}, ErrUnknown
	}
	if err != nil {
		return Node{}, fmt.Errorf("create vmid %d: %w", request.VMID, err)
	}
	if node.VMID == 0 {
		node.VMID = request.VMID
	}
	if err := m.validate(node.VMID); err != nil {
		return Node{}, err
	}
	return node, nil
}

func (m *Manager) Reconcile(ctx context.Context, node Node) (Node, error) {
	if err := m.validate(node.VMID); err != nil {
		return Node{}, err
	}
	observed, err := m.runtime.Observe(ctx, node.VMID)
	if errors.Is(err, ErrUnknown) {
		return node, ErrUnknown
	}
	if err != nil {
		return Node{}, fmt.Errorf("observe vmid %d: %w", node.VMID, err)
	}
	if observed.VMID == 0 {
		observed.VMID = node.VMID
	}
	return observed, nil
}

func (m *Manager) Start(ctx context.Context, node Node) error {
	if err := m.validate(node.VMID); err != nil {
		return err
	}
	if node.State == NodeUnknown {
		return ErrUnknownUnresolved
	}
	if err := m.runtime.Start(ctx, node.VMID); errors.Is(err, ErrUnknown) {
		return ErrUnknown
	} else if err != nil {
		return fmt.Errorf("start vmid %d: %w", node.VMID, err)
	}
	return nil
}

func (m *Manager) Join(ctx context.Context, node Node) error {
	if err := m.validate(node.VMID); err != nil {
		return err
	}
	if node.State == NodeUnknown {
		return ErrUnknownUnresolved
	}
	if err := m.runtime.Join(ctx, node.VMID); errors.Is(err, ErrUnknown) {
		return ErrUnknown
	} else if err != nil {
		return fmt.Errorf("join vmid %d: %w", node.VMID, err)
	}
	return nil
}

func (m *Manager) Stop(ctx context.Context, node Node) error {
	if err := m.validate(node.VMID); err != nil {
		return err
	}
	if node.State == NodeUnknown {
		return ErrUnknownUnresolved
	}
	if !node.Drained || !node.NodeRemoved {
		return ErrLifecycleNotReady
	}
	if err := m.runtime.Stop(ctx, node.VMID); errors.Is(err, ErrUnknown) {
		return ErrUnknown
	} else if err != nil {
		return fmt.Errorf("stop vmid %d: %w", node.VMID, err)
	}
	return nil
}

func (m *Manager) Destroy(ctx context.Context, node Node) error {
	if err := m.validate(node.VMID); err != nil {
		return err
	}
	if node.State == NodeUnknown {
		return ErrUnknownUnresolved
	}
	if node.State != NodeStopped || !node.Drained || !node.NodeRemoved {
		return ErrLifecycleNotReady
	}
	if err := m.runtime.Destroy(ctx, node.VMID); errors.Is(err, ErrUnknown) {
		return ErrUnknown
	} else if err != nil {
		return fmt.Errorf("destroy vmid %d: %w", node.VMID, err)
	}
	return nil
}
