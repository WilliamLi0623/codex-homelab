package orchestrator

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/WilliamLi0623/codex-homelab/internal/executor/k3s"
	"github.com/WilliamLi0623/codex-homelab/internal/store"
)

func TestObservationLoopSkipsRunningAndCompletesSucceededOnce(t *testing.T) {
	database, task, attempt := resultConsumerFixture(t)
	if _, _, err := database.EnsureAttemptExecutionSpec(context.Background(), store.AttemptExecutionSpec{TaskID: task.ID, AttemptID: attempt.ID, Branch: "refs/heads/codex/task/attempt", ValidationCommand: []string{"go", "test"}}); err != nil {
		t.Fatal(err)
	}
	results := observingResults{job: k3s.Job{AttemptID: attempt.ID, State: k3s.JobRunning}}
	consumer := NewResultConsumer(database, results, &releaseCapacity{})
	loop := NewObservationLoop(database, consumer)
	report, err := loop.RunOnce(context.Background())
	if err != nil || report.Inspected != 1 || report.NotReady != 1 || report.Completed != 0 {
		t.Fatalf("running report = (%+v, %v)", report, err)
	}
	results = observingResults{result: k3s.Result{CommitSHA: resultTestCommitSHA}, job: k3s.Job{AttemptID: attempt.ID, State: k3s.JobRunning}}
	consumer = NewResultConsumer(database, results, &releaseCapacity{})
	loop = NewObservationLoop(database, consumer)
	report, err = loop.RunOnce(context.Background())
	if err != nil || report.Completed != 1 || report.Inspected != 1 {
		t.Fatalf("succeeded report = (%+v, %v)", report, err)
	}
	report, err = loop.RunOnce(context.Background())
	if err != nil || report.Inspected != 0 {
		t.Fatalf("replay report = (%+v, %v)", report, err)
	}
}

func TestObservationLoopCountsUnknownWithoutAdvancing(t *testing.T) {
	database, task, attempt := resultConsumerFixture(t)
	if _, _, err := database.EnsureAttemptExecutionSpec(context.Background(), store.AttemptExecutionSpec{TaskID: task.ID, AttemptID: attempt.ID, Branch: "refs/heads/codex/task/attempt", ValidationCommand: []string{"go", "test"}}); err != nil {
		t.Fatal(err)
	}
	results := observingResults{job: k3s.Job{AttemptID: attempt.ID, State: k3s.JobUnknown}}
	loop := NewObservationLoop(database, NewResultConsumer(database, results, &releaseCapacity{}))
	report, err := loop.RunOnce(context.Background())
	if err != nil || report.Unknown != 1 || len(report.Errors) != 1 {
		t.Fatalf("unknown report = (%+v, %v)", report, err)
	}
}

func TestObservationLoopRejectsInvalidConfiguration(t *testing.T) {
	if _, err := (*ObservationLoop)(nil).RunOnce(context.Background()); !errors.Is(err, ErrObservationUnavailable) {
		t.Fatalf("nil loop error = %v", err)
	}
	database, err := store.Open(filepath.Join(t.TempDir(), "controller.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	loop := NewObservationLoop(database, nil)
	if _, err := loop.RunOnce(context.Background()); !errors.Is(err, ErrObservationUnavailable) {
		t.Fatalf("unconfigured loop error = %v", err)
	}
}
