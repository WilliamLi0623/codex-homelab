package sessionruntime

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sync"
	"time"

	"github.com/WilliamLi0623/codex-homelab/internal/store"
)

var (
	ErrBootstrapConfiguration         = errors.New("session bootstrap configuration is invalid")
	ErrBootstrapBinding               = errors.New("session bootstrap binding is stale or mismatched")
	ErrBootstrapCheckpointPersistence = errors.New("session bootstrap UNKNOWN checkpoint persistence could not be confirmed")
	bootstrapDigestPattern            = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

const bootstrapPersistenceTimeout = 3 * time.Second

// BootstrapDriver owns the external per-stage action and its read-only
// reconciliation. Apply must not be invoked unless the store granted a fresh
// stage claim. VerifyIdentity confirms the target is still owned by binding.
type BootstrapDriver interface {
	Apply(context.Context, store.SessionRuntimeBinding, store.SessionBootstrapStage) (store.SessionBootstrapEvidence, error)
	Observe(context.Context, store.SessionRuntimeBinding, store.SessionBootstrapStage) (store.SessionBootstrapEvidence, bool, error)
	VerifyIdentity(context.Context, store.SessionRuntimeBinding) error
}

// BootstrapCoordinator records and reconciles the ordered guest production
// bootstrap. Completion here says nothing about runtime readiness or account
// login; those remain separate checks.
type BootstrapCoordinator struct {
	store  *store.Store
	driver BootstrapDriver
	mu     sync.Mutex
}

func NewBootstrapCoordinator(db *store.Store, driver BootstrapDriver) (*BootstrapCoordinator, error) {
	if db == nil || driver == nil {
		return nil, ErrBootstrapConfiguration
	}
	return &BootstrapCoordinator{store: db, driver: driver}, nil
}

// Ensure advances the fixed ordered production bootstrap for the exact
// current binding. Existing INTENT or UNKNOWN stages are only observed; they
// are never applied again.
func (c *BootstrapCoordinator) Ensure(ctx context.Context, expected store.SessionRuntimeBinding) error {
	if c == nil || c.store == nil || c.driver == nil || ctx == nil || !validBootstrapBinding(expected) {
		return ErrBootstrapConfiguration
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, stage := range bootstrapStages() {
		current, err := c.currentBinding(ctx, expected)
		if err != nil {
			return err
		}
		checkpoint, newlyClaimed, err := c.store.BeginSessionBootstrapStage(ctx, current.ID, current.Generation, stage)
		if err != nil {
			return c.stageFailure(ctx, current, stage, "checkpoint")
		}
		if checkpoint.Status == store.SessionBootstrapComplete {
			continue
		}
		if checkpoint.Status != store.SessionBootstrapIntent && checkpoint.Status != store.SessionBootstrapUnknown {
			return bootstrapStageError(stage, "checkpoint")
		}

		current, err = c.currentBinding(ctx, expected)
		if err != nil {
			if newlyClaimed {
				if persistErr := c.preserveUnknown(ctx, expected, stage); persistErr != nil {
					return bootstrapCheckpointFailure(stage, "binding")
				}
			}
			return err
		}
		if err := c.driver.VerifyIdentity(ctx, current); err != nil {
			return c.stageFailure(ctx, expected, stage, "driver_identity")
		}
		// Re-read after ownership verification so a replaced generation cannot
		// inherit authority from a check against its predecessor.
		current, err = c.currentBinding(ctx, expected)
		if err != nil {
			if newlyClaimed {
				if persistErr := c.preserveUnknown(ctx, expected, stage); persistErr != nil {
					return bootstrapCheckpointFailure(stage, "binding")
				}
			}
			return err
		}

		if newlyClaimed {
			evidence, applyErr := c.driver.Apply(ctx, current, stage)
			if applyErr != nil {
				return c.stageFailure(ctx, expected, stage, "driver")
			}
			if !validBootstrapEvidence(evidence) {
				return c.stageFailure(ctx, expected, stage, "invalid_evidence")
			}
			if err := c.store.CompleteSessionBootstrapStage(ctx, current.ID, current.Generation, stage, store.SessionBootstrapIntent, evidence); err != nil {
				return c.stageFailure(ctx, expected, stage, "checkpoint")
			}
			continue
		}

		evidence, verified, observeErr := c.driver.Observe(ctx, current, stage)
		if observeErr != nil {
			return c.stageFailure(ctx, expected, stage, "driver")
		}
		if !verified {
			return c.stageFailure(ctx, expected, stage, "outcome_unknown")
		}
		if !validBootstrapEvidence(evidence) {
			return c.stageFailure(ctx, expected, stage, "invalid_evidence")
		}
		if err := c.store.CompleteSessionBootstrapStage(ctx, current.ID, current.Generation, stage, checkpoint.Status, evidence); err != nil {
			return c.stageFailure(ctx, expected, stage, "checkpoint")
		}
	}
	return nil
}

// Reconcile resolves only checkpoints that already exist. It never creates a
// claim and never calls Apply, making it suitable for resume and UNKNOWN paths.
func (c *BootstrapCoordinator) Reconcile(ctx context.Context, expected store.SessionRuntimeBinding) error {
	if c == nil || c.store == nil || c.driver == nil || ctx == nil || !validBootstrapBinding(expected) {
		return ErrBootstrapConfiguration
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	stages := bootstrapStages()
	checkpoints := make([]store.SessionBootstrapCheckpoint, len(stages))
	for i, stage := range stages {
		current, err := c.currentBinding(ctx, expected)
		if err != nil {
			return err
		}
		checkpoint, err := c.store.GetSessionBootstrapStage(ctx, current.ID, current.Generation, stage)
		if err != nil {
			return bootstrapStageError(stage, "checkpoint_missing")
		}
		if checkpoint.Status != store.SessionBootstrapComplete && checkpoint.Status != store.SessionBootstrapIntent && checkpoint.Status != store.SessionBootstrapUnknown {
			return bootstrapStageError(stage, "checkpoint")
		}
		if checkpoint.Status == store.SessionBootstrapComplete && !validBootstrapEvidence(checkpoint.Evidence) {
			return bootstrapStageError(stage, "invalid_evidence")
		}
		checkpoints[i] = checkpoint
	}

	for i, stage := range stages {
		checkpoint := checkpoints[i]
		if checkpoint.Status == store.SessionBootstrapComplete {
			continue
		}
		current, err := c.currentBinding(ctx, expected)
		if err != nil {
			return err
		}
		if err := c.driver.VerifyIdentity(ctx, current); err != nil {
			return c.stageFailure(ctx, expected, stage, "driver_identity")
		}
		current, err = c.currentBinding(ctx, expected)
		if err != nil {
			if persistErr := c.preserveUnknown(ctx, expected, stage); persistErr != nil {
				return bootstrapCheckpointFailure(stage, "binding")
			}
			return err
		}
		evidence, verified, observeErr := c.driver.Observe(ctx, current, stage)
		if observeErr != nil {
			return c.stageFailure(ctx, expected, stage, "driver")
		}
		if !verified {
			return c.stageFailure(ctx, expected, stage, "outcome_unknown")
		}
		if !validBootstrapEvidence(evidence) {
			return c.stageFailure(ctx, expected, stage, "invalid_evidence")
		}
		if err := c.store.CompleteSessionBootstrapStage(ctx, current.ID, current.Generation, stage, checkpoint.Status, evidence); err != nil {
			return c.stageFailure(ctx, expected, stage, "checkpoint")
		}
	}
	return nil
}

// VerifyReady proves that every completed bootstrap checkpoint still matches a
// fresh read-only observation for the exact runtime generation. It never
// applies a stage or changes checkpoint state. This is technical guest/App
// Server transport readiness only; account identity and authenticated model
// turns remain separate acceptance gates.
func (c *BootstrapCoordinator) VerifyReady(ctx context.Context, expected store.SessionRuntimeBinding) error {
	if c == nil || c.store == nil || c.driver == nil || ctx == nil || !validBootstrapBinding(expected) {
		return ErrBootstrapConfiguration
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	checkpoints := make([]store.SessionBootstrapCheckpoint, len(bootstrapStages()))
	for i, stage := range bootstrapStages() {
		current, err := c.currentBinding(ctx, expected)
		if err != nil {
			return err
		}
		checkpoint, err := c.store.GetSessionBootstrapStage(ctx, current.ID, current.Generation, stage)
		if err != nil || checkpoint.Status != store.SessionBootstrapComplete || !validBootstrapEvidence(checkpoint.Evidence) {
			return bootstrapStageError(stage, "readiness_checkpoint")
		}
		checkpoints[i] = checkpoint
	}

	current, err := c.currentBinding(ctx, expected)
	if err != nil {
		return err
	}
	if err := c.driver.VerifyIdentity(ctx, current); err != nil {
		return ErrBootstrapBinding
	}
	for i, stage := range bootstrapStages() {
		current, err = c.currentBinding(ctx, expected)
		if err != nil {
			return err
		}
		evidence, verified, observeErr := c.driver.Observe(ctx, current, stage)
		if observeErr != nil || !verified || !validBootstrapEvidence(evidence) || evidence != checkpoints[i].Evidence {
			return bootstrapStageError(stage, "readiness_observation")
		}
	}
	return nil
}

func (c *BootstrapCoordinator) currentBinding(ctx context.Context, expected store.SessionRuntimeBinding) (store.SessionRuntimeBinding, error) {
	current, err := c.store.GetSessionRuntimeBinding(ctx, expected.SessionID, expected.EpochID)
	if err != nil || current.State == "DELETED" || current.ID != expected.ID || current.SessionID != expected.SessionID || current.EpochID != expected.EpochID || current.Generation != expected.Generation || current.VMID != expected.VMID {
		return store.SessionRuntimeBinding{}, ErrBootstrapBinding
	}
	return current, nil
}

func (c *BootstrapCoordinator) preserveUnknown(ctx context.Context, binding store.SessionRuntimeBinding, stage store.SessionBootstrapStage) error {
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), bootstrapPersistenceTimeout)
	defer cancel()
	if err := c.store.MarkSessionBootstrapStageUnknown(persistCtx, binding.ID, binding.Generation, stage, store.SessionBootstrapIntent); err == nil {
		return nil
	}
	checkpoint, err := c.store.GetSessionBootstrapStage(persistCtx, binding.ID, binding.Generation, stage)
	if err == nil && checkpoint.Generation == binding.Generation && checkpoint.Status == store.SessionBootstrapUnknown {
		return nil
	}
	return ErrBootstrapCheckpointPersistence
}

func (c *BootstrapCoordinator) stageFailure(ctx context.Context, binding store.SessionRuntimeBinding, stage store.SessionBootstrapStage, class string) error {
	if err := c.preserveUnknown(ctx, binding, stage); err != nil {
		return bootstrapCheckpointFailure(stage, class)
	}
	return bootstrapStageError(stage, class)
}

func bootstrapCheckpointFailure(stage store.SessionBootstrapStage, class string) error {
	return fmt.Errorf("bootstrap stage %s %s; durable UNKNOWN checkpoint could not be confirmed: %w", stage, class, errors.Join(ErrOutcomeUnknown, ErrBootstrapCheckpointPersistence))
}

func bootstrapStages() []store.SessionBootstrapStage {
	return []store.SessionBootstrapStage{
		store.SessionBootstrapIsolation,
		store.SessionBootstrapGuestIdentity,
		store.SessionBootstrapHostPin,
		store.SessionBootstrapImageBackup,
		store.SessionBootstrapImageSanitized,
		store.SessionBootstrapNetworkEnabled,
		store.SessionBootstrapArtifactVerified,
		store.SessionBootstrapTransportVerified,
	}
}

func validBootstrapBinding(binding store.SessionRuntimeBinding) bool {
	return binding.ID != "" && binding.SessionID != "" && binding.EpochID != "" && binding.Generation != "" && binding.VMID >= store.SessionVMIDMin && binding.VMID <= store.SessionVMIDMax
}

func validBootstrapEvidence(evidence store.SessionBootstrapEvidence) bool {
	return bootstrapDigestPattern.MatchString(evidence.SHA256)
}

func bootstrapStageError(stage store.SessionBootstrapStage, class string) error {
	return fmt.Errorf("bootstrap stage %s %s: %w", stage, class, ErrOutcomeUnknown)
}
