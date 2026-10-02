package sessionruntime

import (
	"context"
	"fmt"

	"github.com/WilliamLi0623/codex-homelab/internal/store"
)

type bootstrapBoundStage interface {
	Apply(context.Context, store.SessionRuntimeBinding) (store.SessionBootstrapEvidence, error)
	Observe(context.Context, store.SessionRuntimeBinding) (store.SessionBootstrapEvidence, bool, error)
}

type bootstrapStageDriver struct {
	stages         map[store.SessionBootstrapStage]bootstrapBoundStage
	verifyIdentity func(context.Context, store.SessionRuntimeBinding) error
}

func newBootstrapStageDriver(stages map[store.SessionBootstrapStage]bootstrapBoundStage, verifyIdentity func(context.Context, store.SessionRuntimeBinding) error) (*bootstrapStageDriver, error) {
	if verifyIdentity == nil || len(stages) != len(bootstrapStages()) {
		return nil, ErrBootstrapConfiguration
	}
	owned := make(map[store.SessionBootstrapStage]bootstrapBoundStage, len(stages))
	for _, stage := range bootstrapStages() {
		adapter, ok := stages[stage]
		if !ok || adapter == nil {
			return nil, ErrBootstrapConfiguration
		}
		owned[stage] = adapter
	}
	return &bootstrapStageDriver{stages: owned, verifyIdentity: verifyIdentity}, nil
}

func (d *bootstrapStageDriver) Apply(ctx context.Context, binding store.SessionRuntimeBinding, stage store.SessionBootstrapStage) (store.SessionBootstrapEvidence, error) {
	adapter, err := d.stage(stage)
	if err != nil {
		return store.SessionBootstrapEvidence{}, err
	}
	return adapter.Apply(ctx, binding)
}

func (d *bootstrapStageDriver) Observe(ctx context.Context, binding store.SessionRuntimeBinding, stage store.SessionBootstrapStage) (store.SessionBootstrapEvidence, bool, error) {
	adapter, err := d.stage(stage)
	if err != nil {
		return store.SessionBootstrapEvidence{}, false, err
	}
	return adapter.Observe(ctx, binding)
}

func (d *bootstrapStageDriver) VerifyIdentity(ctx context.Context, binding store.SessionRuntimeBinding) error {
	if d == nil || d.verifyIdentity == nil {
		return ErrBootstrapConfiguration
	}
	return d.verifyIdentity(ctx, binding)
}

func (d *bootstrapStageDriver) stage(stage store.SessionBootstrapStage) (bootstrapBoundStage, error) {
	if d == nil || d.stages == nil {
		return nil, ErrBootstrapConfiguration
	}
	adapter, ok := d.stages[stage]
	if !ok || adapter == nil {
		return nil, fmt.Errorf("unknown bootstrap stage %q: %w", stage, ErrBootstrapConfiguration)
	}
	return adapter, nil
}
