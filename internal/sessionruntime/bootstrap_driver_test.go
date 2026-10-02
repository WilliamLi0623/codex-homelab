package sessionruntime

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/WilliamLi0623/codex-homelab/internal/store"
)

type bootstrapBoundStageFake struct {
	name        string
	applyCall   int
	observeCall int
	applyErr    error
	observeErr  error
	verified    bool
}

func (s *bootstrapBoundStageFake) Apply(context.Context, store.SessionRuntimeBinding) (store.SessionBootstrapEvidence, error) {
	s.applyCall++
	if s.applyErr != nil {
		return store.SessionBootstrapEvidence{}, s.applyErr
	}
	return store.SessionBootstrapEvidence{SHA256: bootstrapTestSHA256}, nil
}

func (s *bootstrapBoundStageFake) Observe(context.Context, store.SessionRuntimeBinding) (store.SessionBootstrapEvidence, bool, error) {
	s.observeCall++
	if s.observeErr != nil {
		return store.SessionBootstrapEvidence{}, false, s.observeErr
	}
	return store.SessionBootstrapEvidence{SHA256: bootstrapTestSHA256}, s.verified, nil
}

func TestBootstrapStageDriverDispatchesEachOrderedStage(t *testing.T) {
	stages := make(map[store.SessionBootstrapStage]bootstrapBoundStage, len(bootstrapStages()))
	for _, name := range bootstrapStages() {
		stages[name] = &bootstrapBoundStageFake{name: string(name), verified: true}
	}
	verifiedBinding := store.SessionRuntimeBinding{ID: "binding-1", SessionID: "session-1", EpochID: "epoch-1", Generation: "generation-1", VMID: 4010}
	verifyCalls := 0
	driver, err := newBootstrapStageDriver(stages, func(_ context.Context, got store.SessionRuntimeBinding) error {
		verifyCalls++
		if got != verifiedBinding {
			return ErrBootstrapBinding
		}
		return nil
	})
	if err != nil {
		t.Fatalf("newBootstrapStageDriver() error: %v", err)
	}
	if err := driver.VerifyIdentity(context.Background(), verifiedBinding); err != nil {
		t.Fatalf("VerifyIdentity() error: %v", err)
	}
	for _, stage := range bootstrapStages() {
		evidence, err := driver.Apply(context.Background(), verifiedBinding, stage)
		if err != nil || evidence.SHA256 != bootstrapTestSHA256 {
			t.Fatalf("Apply(%s) evidence=%+v error=%v", stage, evidence, err)
		}
		observed, ok, err := driver.Observe(context.Background(), verifiedBinding, stage)
		if err != nil || !ok || observed != evidence {
			t.Fatalf("Observe(%s) evidence=%+v ok=%t error=%v", stage, observed, ok, err)
		}
		fake := stages[stage].(*bootstrapBoundStageFake)
		if fake.applyCall != 1 || fake.observeCall != 1 {
			t.Fatalf("stage %s Apply/Observe calls=%d/%d, want 1/1", stage, fake.applyCall, fake.observeCall)
		}
	}
	if verifyCalls != 1 {
		t.Fatalf("VerifyIdentity calls=%d, want 1", verifyCalls)
	}
}

func TestBootstrapStageDriverRejectsIncompleteOrUnexpectedStageSet(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(map[store.SessionBootstrapStage]bootstrapBoundStage)
	}{
		{name: "missing transport", mutate: func(stages map[store.SessionBootstrapStage]bootstrapBoundStage) {
			delete(stages, store.SessionBootstrapTransportVerified)
		}},
		{name: "unexpected stage", mutate: func(stages map[store.SessionBootstrapStage]bootstrapBoundStage) {
			stages[store.SessionBootstrapStage("not-a-stage")] = &bootstrapBoundStageFake{}
		}},
		{name: "nil stage", mutate: func(stages map[store.SessionBootstrapStage]bootstrapBoundStage) {
			stages[store.SessionBootstrapIsolation] = nil
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stages := make(map[store.SessionBootstrapStage]bootstrapBoundStage, len(bootstrapStages()))
			for _, stage := range bootstrapStages() {
				stages[stage] = &bootstrapBoundStageFake{}
			}
			tc.mutate(stages)
			if _, err := newBootstrapStageDriver(stages, func(context.Context, store.SessionRuntimeBinding) error { return nil }); !errors.Is(err, ErrBootstrapConfiguration) {
				t.Fatalf("newBootstrapStageDriver() error=%v, want ErrBootstrapConfiguration", err)
			}
		})
	}
}

func TestBootstrapStageDriverDoesNotHideStageErrors(t *testing.T) {
	stages := make(map[store.SessionBootstrapStage]bootstrapBoundStage, len(bootstrapStages()))
	for _, stage := range bootstrapStages() {
		stages[stage] = &bootstrapBoundStageFake{verified: true}
	}
	wantErr := errors.New("sentinel stage failure")
	stages[store.SessionBootstrapArtifactVerified] = &bootstrapBoundStageFake{applyErr: wantErr}
	driver, err := newBootstrapStageDriver(stages, func(context.Context, store.SessionRuntimeBinding) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := driver.Apply(context.Background(), store.SessionRuntimeBinding{}, store.SessionBootstrapArtifactVerified); !errors.Is(err, wantErr) {
		t.Fatalf("Apply() error=%v, want wrapped sentinel", err)
	}
	if _, _, err := driver.Observe(context.Background(), store.SessionRuntimeBinding{}, store.SessionBootstrapStage("unknown")); err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("Observe(unknown) error=%v, want explicit unknown-stage failure", err)
	}
}
