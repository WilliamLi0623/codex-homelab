package orchestrator

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/WilliamLi0623/codex-homelab/internal/domain"
	"github.com/WilliamLi0623/codex-homelab/internal/executor/k3s"
	"github.com/WilliamLi0623/codex-homelab/internal/store"
)

const resultTestCommitSHA = "0123456789abcdef0123456789abcdef01234567"

type resultCollector struct {
	result k3s.Result
	calls  int
}

func (c *resultCollector) CollectResult(context.Context, string) (k3s.Result, error) {
	c.calls++
	return c.result, nil
}

type cancellableResultCollector struct {
	result k3s.Result
	calls  int
	events *[]string
	err    error
}

func (c *cancellableResultCollector) CollectResult(context.Context, string) (k3s.Result, error) {
	c.calls++
	*c.events = append(*c.events, "collect")
	return c.result, nil
}

func (c *cancellableResultCollector) Cancel(context.Context, string) error {
	*c.events = append(*c.events, "cancel")
	return c.err
}

type releaseCapacity struct {
	calls  int
	err    error
	events *[]string
}

type observingResults struct {
	result k3s.Result
	job    k3s.Job
}

func (o observingResults) CollectResult(context.Context, string) (k3s.Result, error) {
	return o.result, nil
}
func (o observingResults) Observe(context.Context, string) (k3s.Job, error) { return o.job, nil }

func TestResultConsumerObserveAndCompleteRequiresSucceededJob(t *testing.T) {
	database, task, attempt := resultConsumerFixture(t)
	input := CompletionInput{TaskID: task.ID, AttemptID: attempt.ID, Branch: "refs/heads/task-1", ValidationCommand: []string{"go", "test"}}
	consumer := NewResultConsumer(database, observingResults{job: k3s.Job{AttemptID: attempt.ID, State: k3s.JobPending}}, &releaseCapacity{})
	if _, _, err := consumer.ObserveAndComplete(context.Background(), input); !errors.Is(err, ErrCompletionNotReady) {
		t.Fatalf("pending error = %v", err)
	}
	consumer = NewResultConsumer(database, observingResults{job: k3s.Job{AttemptID: attempt.ID, State: k3s.JobRunning}}, &releaseCapacity{})
	if _, _, err := consumer.ObserveAndComplete(context.Background(), input); !errors.Is(err, ErrCompletionNotReady) {
		t.Fatalf("running without result error = %v", err)
	}
}

func TestResultConsumerCompletesRunningJobWhenResultIsReady(t *testing.T) {
	database, task, attempt := resultConsumerFixture(t)
	input := CompletionInput{TaskID: task.ID, AttemptID: attempt.ID, Branch: "refs/heads/task-1", ValidationCommand: []string{"go", "test"}}
	releaser := &releaseCapacity{}
	consumer := NewResultConsumer(database, observingResults{result: k3s.Result{CommitSHA: resultTestCommitSHA}, job: k3s.Job{AttemptID: attempt.ID, State: k3s.JobRunning}}, releaser)
	_, completion, err := consumer.ObserveAndComplete(context.Background(), input)
	if err != nil {
		t.Fatalf("running ready result error = %v", err)
	}
	if completion.CommitSHA != resultTestCommitSHA || releaser.calls != 1 {
		t.Fatalf("completion=%+v release calls=%d", completion, releaser.calls)
	}
}

func TestResultConsumerObserveAndCompleteStoredRequiresPersistedSpec(t *testing.T) {
	database, task, attempt := resultConsumerFixture(t)
	consumer := NewResultConsumer(database, observingResults{job: k3s.Job{AttemptID: attempt.ID, State: k3s.JobRunning}}, &releaseCapacity{})
	if _, _, err := consumer.ObserveAndCompleteStored(context.Background(), task.ID, attempt.ID); !errors.Is(err, store.ErrExecutionSpecNotFound) {
		t.Fatalf("missing spec error = %v", err)
	}
	if _, _, err := database.EnsureAttemptExecutionSpec(context.Background(), store.AttemptExecutionSpec{TaskID: task.ID, AttemptID: attempt.ID, Branch: "refs/heads/codex/task/attempt", ValidationCommand: []string{"go", "test"}}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := consumer.ObserveAndCompleteStored(context.Background(), task.ID, attempt.ID); !errors.Is(err, ErrCompletionNotReady) {
		t.Fatalf("stored running error = %v", err)
	}
}

func (c *releaseCapacity) Create(context.Context, ClaimRequest) (Claim, error) { return Claim{}, nil }
func (c *releaseCapacity) Release(context.Context, Claim) error {
	if c.events != nil {
		*c.events = append(*c.events, "release")
	}
	c.calls++
	return c.err
}

func TestResultConsumerPersistsCompletionBeforeReleasingCapacity(t *testing.T) {
	database, task, attempt := resultConsumerFixture(t)
	events := []string{}
	collector := &cancellableResultCollector{result: k3s.Result{CommitSHA: resultTestCommitSHA}, events: &events}
	releaser := &releaseCapacity{events: &events}
	consumer := NewResultConsumer(database, collector, releaser)

	got, err := consumer.Complete(context.Background(), CompletionInput{TaskID: task.ID, AttemptID: attempt.ID, Branch: "refs/heads/task-1", ValidationCommand: []string{"go", "test", "./..."}})
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if got.CommitSHA != resultTestCommitSHA || collector.calls != 1 || releaser.calls != 1 {
		t.Fatalf("completion=%+v release=%d events=%v", got, releaser.calls, events)
	}
	if want := []string{"collect", "cancel", "release"}; !reflect.DeepEqual(events, want) {
		t.Fatalf("events=%v, want %v", events, want)
	}
	if _, err := database.GetValidationResult(context.Background(), got.ID); err != nil {
		t.Fatalf("validation result missing: %v", err)
	}
}

func TestResultConsumerFailsClosedWhenCancellingExecutionFails(t *testing.T) {
	database, task, attempt := resultConsumerFixture(t)
	events := []string{}
	collector := &cancellableResultCollector{result: k3s.Result{CommitSHA: resultTestCommitSHA}, events: &events, err: errors.New("cancel failed")}
	releaser := &releaseCapacity{events: &events}
	consumer := NewResultConsumer(database, collector, releaser)

	_, err := consumer.Complete(context.Background(), CompletionInput{TaskID: task.ID, AttemptID: attempt.ID, Branch: "refs/heads/task-1", ValidationCommand: []string{"go", "test"}})
	if !errors.Is(err, ErrCompletionReleasePending) {
		t.Fatalf("Complete() error = %v, want release pending", err)
	}
	if releaser.calls != 0 {
		t.Fatalf("release calls = %d, want 0", releaser.calls)
	}
	if want := []string{"collect", "cancel"}; !reflect.DeepEqual(events, want) {
		t.Fatalf("events=%v, want %v", events, want)
	}
}

func TestResultConsumerDoesNotRecollectAfterDurableCompletionWhenReleaseWasUnknown(t *testing.T) {
	database, task, attempt := resultConsumerFixture(t)
	collector := &resultCollector{result: k3s.Result{CommitSHA: resultTestCommitSHA}}
	releaser := &releaseCapacity{err: errors.New("release outcome unknown")}
	consumer := NewResultConsumer(database, collector, releaser)
	input := CompletionInput{TaskID: task.ID, AttemptID: attempt.ID, Branch: "refs/heads/task-1", ValidationCommand: []string{"go", "test"}}
	if _, err := consumer.Complete(context.Background(), input); !errors.Is(err, ErrCompletionReleasePending) {
		t.Fatalf("first Complete() error = %v, want release pending", err)
	}
	if collector.calls != 1 {
		t.Fatalf("collector calls = %d, want 1", collector.calls)
	}
	releaser.err = nil
	if _, err := consumer.Complete(context.Background(), input); err != nil {
		t.Fatalf("reconciled Complete() error = %v", err)
	}
	if collector.calls != 1 {
		t.Fatalf("collector calls after replay = %d, want 1", collector.calls)
	}
}

func TestResultConsumerRejectsMissingCommitAndUnknownHandle(t *testing.T) {
	database, task, attempt := resultConsumerFixture(t)
	collector := &resultCollector{}
	releaser := &releaseCapacity{}
	consumer := NewResultConsumer(database, collector, releaser)
	input := CompletionInput{TaskID: task.ID, AttemptID: attempt.ID, Branch: "main", ValidationCommand: []string{"go", "test"}}
	if _, err := consumer.Complete(context.Background(), input); !errors.Is(err, ErrCompletionResultMissing) {
		t.Fatalf("missing commit error = %v", err)
	}
	if err := database.UpdateExecutionHandleState(context.Background(), "job-1", string(k3s.HandleUnknown)); err != nil {
		t.Fatal(err)
	}
	collector.result.CommitSHA = resultTestCommitSHA
	if _, err := consumer.Complete(context.Background(), input); !errors.Is(err, k3s.ErrUnknownUnresolved) {
		t.Fatalf("unknown handle error = %v", err)
	}
}

func resultConsumerFixture(t *testing.T) (*store.Store, domain.Task, domain.Attempt) {
	t.Helper()
	database, err := store.Open(filepath.Join(t.TempDir(), "controller.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	task, err := func() (domain.Task, error) {
		created := domain.NewTask("consumer-task", "owner/repo", "main", "change", "consumer-request")
		got, _, err := database.CreateTask(context.Background(), created)
		return got, err
	}()
	if err != nil {
		t.Fatal(err)
	}
	_, attempt, err := database.StartAttempt(context.Background(), task.ID, "openai-primary")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := database.RecordExecutionHandle(context.Background(), attempt.ID, "k3s", "job-1", string(k3s.HandleRunning)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := database.ClaimCapacity(context.Background(), store.CapacityClaimRequest{TaskID: task.ID, AttemptID: attempt.ID, Generation: "gen-1", Priority: 1}); err != nil {
		t.Fatal(err)
	}
	return database, task, attempt
}
