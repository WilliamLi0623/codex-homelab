package store

import (
	"context"
	"errors"
	"testing"
)

func TestReleaseProgressIsIdempotentAndPreservesIdentity(t *testing.T) {
	s := openCapacityTestStore(t)
	request := ReleaseProgressRequest{TaskID: "task", AttemptID: "attempt", VMID: 3010, Generation: "gen-1", KubeNode: "node-3010"}
	first, created, err := s.EnsureReleaseProgress(context.Background(), request)
	if err != nil || !created || first.Step != ReleaseStepNone || first.State != ReleaseStatePending { t.Fatalf("first EnsureReleaseProgress() = (%+v, %t, %v)", first, created, err) }
	second, created, err := s.EnsureReleaseProgress(context.Background(), request)
	if err != nil || created || second.ID != first.ID { t.Fatalf("second EnsureReleaseProgress() = (%+v, %t, %v)", second, created, err) }
	request.Generation = "other"
	if _, _, err := s.EnsureReleaseProgress(context.Background(), request); !errors.Is(err, ErrReleaseProgressConflict) { t.Fatalf("conflict error = %v", err) }
}

func TestReleaseProgressDoesNotRegressAndPersistsUnknown(t *testing.T) {
	s := openCapacityTestStore(t)
	_, _, err := s.EnsureReleaseProgress(context.Background(), ReleaseProgressRequest{TaskID: "task", AttemptID: "attempt", VMID: 3010, Generation: "gen", KubeNode: "node"})
	if err != nil { t.Fatal(err) }
	if err := s.UpdateReleaseProgress(context.Background(), "task", "attempt", ReleaseStepDrain, ReleaseStateRunning, ""); err != nil { t.Fatal(err) }
	if err := s.UpdateReleaseProgress(context.Background(), "task", "attempt", ReleaseStepDrain, ReleaseStateUnknown, "transport outcome unavailable"); err != nil { t.Fatal(err) }
	got, err := s.GetReleaseProgress(context.Background(), "task", "attempt")
	if err != nil || got.State != ReleaseStateUnknown || got.ErrorSummary != "transport outcome unavailable" { t.Fatalf("stored progress = (%+v, %v)", got, err) }
	if err := s.UpdateReleaseProgress(context.Background(), "task", "attempt", ReleaseStepCordon, ReleaseStatePending, ""); !errors.Is(err, ErrReleaseProgressRegress) { t.Fatalf("regression error = %v", err) }
}

func TestReleaseProgressRejectsUnsafeInputs(t *testing.T) {
	s := openCapacityTestStore(t)
	for _, request := range []ReleaseProgressRequest{{AttemptID: "a", VMID: 3010, Generation: "g", KubeNode: "n"}, {TaskID: "t", AttemptID: "a", VMID: 220, Generation: "g", KubeNode: "n"}} {
		if _, _, err := s.EnsureReleaseProgress(context.Background(), request); !errors.Is(err, ErrReleaseProgressInvalid) { t.Fatalf("request %+v error = %v", request, err) }
	}
}
