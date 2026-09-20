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
	store   *store.Store
	manager *capacity.Manager
	config  CapacityAdapterConfig
}

type CapacityAdapterConfig struct {
	TemplateVMID int
	Priority     int
	Hostname     string
	Storage      string
	Bridge       string
	Cores        int
	MemoryMiB    int
	DiskGiB      int
}

func NewCapacityAdapter(database *store.Store, manager *capacity.Manager, templateVMID int) *CapacityAdapter {
	return NewCapacityAdapterWithConfig(database, manager, CapacityAdapterConfig{TemplateVMID: templateVMID, Priority: 1})
}

func NewCapacityAdapterWithConfig(database *store.Store, manager *capacity.Manager, config CapacityAdapterConfig) *CapacityAdapter {
	return &CapacityAdapter{store: database, manager: manager, config: config}
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
	return Claim{ID: stored.ID, VMID: stored.VMID}, err
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
		TemplateVMID: templateVMID, Hostname: request.Hostname,
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

func deterministicGeneration(taskID, attemptID string) string {
	sum := sha256.Sum256([]byte("codex-homelab/capacity-generation/v1\x00" + taskID + "\x00" + attemptID))
	return fmt.Sprintf("sha256:%x", sum[:])
}

func (a *CapacityAdapter) Release(context.Context, Claim) error {
	return ErrCapacityReleaseUnsafe
}
