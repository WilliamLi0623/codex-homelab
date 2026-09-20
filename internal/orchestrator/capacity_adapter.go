package orchestrator

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"

	"github.com/WilliamLi0623/codex-homelab/internal/capacity"
	"github.com/WilliamLi0623/codex-homelab/internal/store"
)

var (
	ErrCapacityUnknown              = errors.New("capacity claim has unknown external outcome")
	ErrCapacityLedgerReconciliation = errors.New("capacity ledger requires reconciliation")
	ErrCapacityReleaseUnsafe        = errors.New("capacity release is not safe to perform")
)

var _ Capacity = (*CapacityAdapter)(nil)

type CapacityClaimRequest struct {
	TaskID       string
	AttemptID    string
	Generation   string
	Priority     int
	VMID         int
	TemplateVMID int
	Hostname     string
	Storage      string
	Bridge       string
	Cores        int
	MemoryMiB    int
	DiskGiB      int
}

type CapacityAdapter struct {
	store      *store.Store
	manager    *capacity.Manager
	config     CapacityAdapterConfig
	releaseOps capacity.WorkerReleaseOperations
}

type CapacityAdapterConfig struct {
	TemplateVMID      int
	Priority          int
	Hostname          string
	Storage           string
	Bridge            string
	Cores             int
	MemoryMiB         int
	DiskGiB           int
	ReleaseOperations capacity.WorkerReleaseOperations
}

func NewCapacityAdapter(database *store.Store, manager *capacity.Manager, templateVMID int) *CapacityAdapter {
	return NewCapacityAdapterWithConfig(database, manager, CapacityAdapterConfig{TemplateVMID: templateVMID, Priority: 1})
}

func NewCapacityAdapterWithConfig(database *store.Store, manager *capacity.Manager, config CapacityAdapterConfig) *CapacityAdapter {
	return &CapacityAdapter{store: database, manager: manager, config: config, releaseOps: config.ReleaseOperations}
}

func (a *CapacityAdapter) Create(ctx context.Context, request ClaimRequest) (Claim, error) {
	if a == nil || a.store == nil || a.manager == nil || request.TaskID == "" || request.AttemptID == "" || a.config.Priority <= 0 {
		return Claim{}, fmt.Errorf("invalid capacity claim request")
	}
	stored, err := a.claim(ctx, CapacityClaimRequest{
		TaskID: request.TaskID, AttemptID: request.AttemptID,
		Generation: deterministicGeneration(request.TaskID, request.AttemptID),
		Priority:   a.config.Priority, TemplateVMID: a.config.TemplateVMID,
		Hostname: a.config.Hostname, Storage: a.config.Storage, Bridge: a.config.Bridge,
		Cores: a.config.Cores, MemoryMiB: a.config.MemoryMiB, DiskGiB: a.config.DiskGiB,
	})
	kubeNode, identityErr := capacity.DynamicHostname(stored.VMID, stored.Generation)
	if err == nil && identityErr != nil {
		err = identityErr
	}
	return Claim{ID: stored.ID, VMID: stored.VMID, TaskID: stored.TaskID, AttemptID: stored.AttemptID, Generation: stored.Generation, KubeNode: kubeNode}, err
}

func (a *CapacityAdapter) Claim(ctx context.Context, request CapacityClaimRequest) (store.CapacityClaim, error) {
	if a == nil || a.store == nil || a.manager == nil {
		return store.CapacityClaim{}, fmt.Errorf("capacity adapter is not configured")
	}
	return a.claim(ctx, request)
}

func (a *CapacityAdapter) claim(ctx context.Context, request CapacityClaimRequest) (store.CapacityClaim, error) {
	claim, created, err := a.store.ClaimCapacity(ctx, store.CapacityClaimRequest{
		TaskID: request.TaskID, AttemptID: request.AttemptID, Generation: request.Generation,
		Priority: request.Priority, VMID: request.VMID,
	})
	if err != nil {
		return store.CapacityClaim{}, err
	}
	if !created {
		if claim.State == store.CapacityUnknown {
			return claim, ErrCapacityUnknown
		}
		if claim.State == store.CapacityCreating {
			// CREATING is ambiguous after interruption or failed ledger
			// persistence. It requires reconciliation and is never success.
			return claim, fmt.Errorf("%w: claim remains CREATING", ErrCapacityLedgerReconciliation)
		}
		return claim, nil
	}
	if err := a.store.UpdateCapacityClaimState(ctx, claim.TaskID, claim.AttemptID, store.CapacityCreating); err != nil {
		return store.CapacityClaim{}, err
	}
	claim.State = store.CapacityCreating
	templateVMID := request.TemplateVMID
	if templateVMID == 0 {
		templateVMID = a.config.TemplateVMID
	}
	_, err = a.manager.Create(ctx, capacity.CreateRequest{
		VMID: claim.VMID, Generation: claim.Generation, TaskID: claim.TaskID,
		TemplateVMID: templateVMID, Hostname: workerHostname(claim.VMID, claim.Generation),
		Storage: request.Storage, Bridge: request.Bridge, Cores: request.Cores, MemoryMiB: request.MemoryMiB, DiskGiB: request.DiskGiB,
	})
	if errors.Is(err, capacity.ErrUnknown) {
		if persistErr := a.store.UpdateCapacityClaimState(ctx, claim.TaskID, claim.AttemptID, store.CapacityUnknown); persistErr != nil {
			// The durable row can still be CREATING here. Report reconciliation
			// explicitly; never treat this as success or replay Create later.
			claim.State = store.CapacityCreating
			return claim, fmt.Errorf("%w: %w: persist UNKNOWN claim: %w", capacity.ErrUnknown, ErrCapacityLedgerReconciliation, persistErr)
		}
		claim.State = store.CapacityUnknown
		return claim, capacity.ErrUnknown
	}
	if err != nil {
		return claim, err
	}
	if err := a.store.UpdateCapacityClaimState(ctx, claim.TaskID, claim.AttemptID, store.CapacityClaimed); err != nil {
		return claim, fmt.Errorf("%w: persist completed capacity claim: %w", ErrCapacityLedgerReconciliation, err)
	}
	claim.State = store.CapacityClaimed
	return claim, nil
}

func workerHostname(vmid int, generation string) string {
	hostname, err := capacity.DynamicHostname(vmid, generation)
	if err != nil {
		return ""
	}
	return hostname
}

func deterministicGeneration(taskID, attemptID string) string {
	sum := sha256.Sum256([]byte("codex-homelab/capacity-generation/v1\x00" + taskID + "\x00" + attemptID))
	return fmt.Sprintf("sha256:%x", sum[:])
}

func (a *CapacityAdapter) Release(ctx context.Context, claim Claim) error {
	if a == nil || a.store == nil || a.manager == nil || a.releaseOps == nil || claim.ID == "" || claim.TaskID == "" || claim.AttemptID == "" || claim.Generation == "" || claim.KubeNode == "" {
		return ErrCapacityReleaseUnsafe
	}
	stored, err := a.store.GetCapacityClaim(ctx, claim.TaskID, claim.AttemptID)
	if err != nil || stored.ID != claim.ID || stored.VMID != claim.VMID || stored.Generation != claim.Generation || stored.State != store.CapacityClaimed {
		return ErrCapacityReleaseUnsafe
	}
	progress, _, err := a.store.EnsureReleaseProgress(ctx, store.ReleaseProgressRequest{TaskID: claim.TaskID, AttemptID: claim.AttemptID, VMID: claim.VMID, Generation: claim.Generation, KubeNode: claim.KubeNode})
	if err != nil || progress.State == store.ReleaseStateUnknown {
		return ErrCapacityReleaseUnsafe
	}
	if progress.Step == store.ReleaseStepDone && progress.State == store.ReleaseStateCompleted {
		return nil
	}
	start := capacity.ReleaseStepCordon
	if progress.Step != store.ReleaseStepNone {
		if progress.State == store.ReleaseStateUnknown {
			return ErrCapacityReleaseUnsafe
		}
		if progress.State == store.ReleaseStateCompleted {
			start, err = nextReleaseStep(progress.Step)
		} else {
			start, err = releaseStepFromStore(progress.Step)
		}
		if err != nil {
			return err
		}
		if start == "" {
			return a.store.UpdateReleaseProgress(ctx, claim.TaskID, claim.AttemptID, store.ReleaseStepDone, store.ReleaseStateCompleted, "")
		}
	}
	node := capacity.Node{VMID: claim.VMID, Generation: claim.Generation, TaskID: claim.TaskID, KubeNode: claim.KubeNode, State: capacity.NodeStopped}
	checkpoint := func(checkpointCtx context.Context, _ capacity.Node, step capacity.ReleaseStep, stepErr error) error {
		state := store.ReleaseStateCompleted
		summary := ""
		if stepErr != nil {
			state = store.ReleaseStateUnknown
			summary = "external release outcome is unknown"
		}
		return a.store.UpdateReleaseProgress(checkpointCtx, claim.TaskID, claim.AttemptID, string(step), state, summary)
	}
	if err := capacity.ReleaseWorkerFromStep(ctx, node, a.releaseOps, start, checkpoint); err != nil {
		return err
	}
	return a.store.UpdateReleaseProgress(ctx, claim.TaskID, claim.AttemptID, store.ReleaseStepDone, store.ReleaseStateCompleted, "")
}

// ReconcileRelease records an explicit external observation before a later
// Release call may retry an UNKNOWN step.
func (a *CapacityAdapter) ReconcileRelease(ctx context.Context, claim Claim, proof string) error {
	if a == nil || a.store == nil || claim.ID == "" || claim.TaskID == "" || claim.AttemptID == "" || claim.Generation == "" || claim.KubeNode == "" {
		return ErrCapacityReleaseUnsafe
	}
	stored, err := a.store.GetCapacityClaim(ctx, claim.TaskID, claim.AttemptID)
	if err != nil || stored.ID != claim.ID || stored.VMID != claim.VMID || stored.Generation != claim.Generation {
		return ErrCapacityReleaseUnsafe
	}
	return a.store.ReconcileReleaseProgress(ctx, store.ReleaseProgressRequest{TaskID: claim.TaskID, AttemptID: claim.AttemptID, VMID: claim.VMID, Generation: claim.Generation, KubeNode: claim.KubeNode}, proof)
}

func nextReleaseStep(step string) (capacity.ReleaseStep, error) {
	switch step {
	case store.ReleaseStepCordon:
		return capacity.ReleaseStepDrain, nil
	case store.ReleaseStepDrain:
		return capacity.ReleaseStepRemoveNode, nil
	case store.ReleaseStepRemoveNode:
		return capacity.ReleaseStepVerifyNodeRemoved, nil
	case store.ReleaseStepVerifyNodeRemoved:
		return capacity.ReleaseStepVerifyIdentity, nil
	case store.ReleaseStepVerifyIdentity:
		return capacity.ReleaseStepStop, nil
	case store.ReleaseStepStop:
		return capacity.ReleaseStepVerifyStopped, nil
	case store.ReleaseStepVerifyStopped:
		return capacity.ReleaseStepDestroy, nil
	case store.ReleaseStepDestroy:
		return "", nil
	default:
		return "", ErrCapacityReleaseUnsafe
	}
}

func releaseStepFromStore(step string) (capacity.ReleaseStep, error) {
	switch step {
	case store.ReleaseStepCordon:
		return capacity.ReleaseStepCordon, nil
	case store.ReleaseStepDrain:
		return capacity.ReleaseStepDrain, nil
	case store.ReleaseStepRemoveNode:
		return capacity.ReleaseStepRemoveNode, nil
	case store.ReleaseStepVerifyNodeRemoved:
		return capacity.ReleaseStepVerifyNodeRemoved, nil
	case store.ReleaseStepVerifyIdentity:
		return capacity.ReleaseStepVerifyIdentity, nil
	case store.ReleaseStepStop:
		return capacity.ReleaseStepStop, nil
	case store.ReleaseStepVerifyStopped:
		return capacity.ReleaseStepVerifyStopped, nil
	case store.ReleaseStepDestroy:
		return capacity.ReleaseStepDestroy, nil
	default:
		return "", ErrCapacityReleaseUnsafe
	}
}
