package orchestrator

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/WilliamLi0623/codex-homelab/internal/executor/k3s"
	"github.com/WilliamLi0623/codex-homelab/internal/modelrouter"
)

type fakeCapacity struct {
	claims    []Claim
	released  []string
	createErr error
}

func (f *fakeCapacity) Create(_ context.Context, r ClaimRequest) (Claim, error) {
	if f.createErr != nil {
		return Claim{}, f.createErr
	}
	c := Claim{ID: r.AttemptID, VMID: 3010, KubeNode: "codex-node"}
	f.claims = append(f.claims, c)
	return c, nil
}
func (f *fakeCapacity) Release(_ context.Context, c Claim) error {
	f.released = append(f.released, c.ID)
	return nil
}

type fakeExecutor struct {
	jobs      []k3s.JobRequest
	messages  []string
	cancelled []string
	createErr error
	result    k3s.Result
}

func (f *fakeExecutor) CreateJob(_ context.Context, r k3s.JobRequest) (k3s.Job, error) {
	f.jobs = append(f.jobs, r)
	if f.createErr != nil {
		return k3s.Job{}, f.createErr
	}
	return k3s.Job{ID: "job-1", TaskID: r.TaskID, AttemptID: r.AttemptID, State: k3s.JobRunning}, nil
}

func normalWorkerRoute() *modelrouter.ResolvedRoute {
	return &modelrouter.ResolvedRoute{
		Mode: modelrouter.ModeNormal, Generation: 1, Role: modelrouter.RoleWorker,
		Provider: "openai", Model: "gpt-6-luna", WireAPI: modelrouter.WireAPIResponses,
		ReasoningEffort: "high", BaseURL: "https://api.openai.com/v1",
		SecretName: "openai-model-gateway", SecretKey: "api-key",
	}
}

func TestDispatchCarriesRepositoryBaseRefAndWorkspaceContract(t *testing.T) {
	capacity := &fakeCapacity{}
	executor := &fakeExecutor{}
	broker := New(capacity, executor)
	_, err := broker.Dispatch(context.Background(), Request{TaskID: "task-1", AttemptID: "attempt-1", ModelProfile: "worker", Route: normalWorkerRoute(), Prompt: "change", Repository: "owner/repo", BaseRef: "main", WorkspacePath: "/workspace/attempt-1", ValidationCommand: []string{"go", "test", "./..."}})
	if err != nil {
		t.Fatalf("Dispatch() error = %v", err)
	}
	if len(executor.jobs) != 1 || executor.jobs[0].ModelProfile != "worker" || executor.jobs[0].Repository != "owner/repo" || executor.jobs[0].BaseRef != "main" || executor.jobs[0].WorkspacePath != "/workspace/attempt-1" || strings.Join(executor.jobs[0].ValidationCommand, " ") != "go test ./..." {
		t.Fatalf("job request = %+v, want repository/base_ref/workspace contract", executor.jobs)
	}
	if executor.jobs[0].NodeName != "codex-node" {
		t.Fatalf("job node = %q, want codex-node", executor.jobs[0].NodeName)
	}
	if executor.jobs[0].Route == nil || executor.jobs[0].Route.Model != "gpt-6-luna" {
		t.Fatalf("job route = %+v, want frozen normal route", executor.jobs[0].Route)
	}
}

func TestDispatchValidatesRouteBeforeCreatingCapacityOrJob(t *testing.T) {
	capacity := &fakeCapacity{}
	executor := &fakeExecutor{}
	broker := New(capacity, executor)
	route := &modelrouter.ResolvedRoute{
		Mode:            modelrouter.ModeNormal,
		Generation:      0,
		Role:            modelrouter.RoleWorker,
		Provider:        "openai",
		Model:           "gpt-6-luna",
		WireAPI:         modelrouter.WireAPIResponses,
		ReasoningEffort: "high",
		BaseURL:         "https://api.openai.com/v1",
		SecretName:      "openai-model-gateway",
		SecretKey:       "api-key",
	}

	_, err := broker.Dispatch(context.Background(), Request{
		TaskID:    "task-1",
		AttemptID: "attempt-1",
		Route:     route,
	})
	if err == nil {
		t.Fatal("Dispatch() error = nil, want route validation error")
	}
	if len(capacity.claims) != 0 {
		t.Fatalf("capacity claims = %v, want none", capacity.claims)
	}
	if len(executor.jobs) != 0 {
		t.Fatalf("executor jobs = %v, want none", executor.jobs)
	}
}

func TestDispatchRejectsMissingFrozenRouteBeforeCapacityClaim(t *testing.T) {
	capacity := &fakeCapacity{}
	executor := &fakeExecutor{}
	broker := New(capacity, executor)

	_, err := broker.Dispatch(context.Background(), Request{TaskID: "task-1", AttemptID: "attempt-1"})
	if err == nil || !strings.Contains(err.Error(), "frozen worker route is required") {
		t.Fatalf("Dispatch() error = %v, want missing frozen route error", err)
	}
	if len(capacity.claims) != 0 {
		t.Fatalf("capacity claims = %v, want none", capacity.claims)
	}
	if len(executor.jobs) != 0 {
		t.Fatalf("executor jobs = %v, want none", executor.jobs)
	}
}

func TestDispatchRejectsOrchestratorRouteBeforeCapacityClaim(t *testing.T) {
	capacity := &fakeCapacity{}
	executor := &fakeExecutor{}
	broker := New(capacity, executor)
	route := &modelrouter.ResolvedRoute{
		Mode:            modelrouter.ModeQuotaFallback,
		Generation:      1,
		Role:            modelrouter.RoleOrchestrator,
		Provider:        "cch",
		Model:           "muse-spark-1.3-contributor",
		WireAPI:         modelrouter.WireAPIResponses,
		ReasoningEffort: "xhigh",
		BaseURL:         "https://cch-jp.zenkexi.com/v1",
		SecretName:      "cch-model-gateway",
		SecretKey:       "api-key",
	}

	_, err := broker.Dispatch(context.Background(), Request{TaskID: "task-1", AttemptID: "attempt-1", Route: route})
	if err == nil {
		t.Fatal("Dispatch() error = nil, want worker-role validation error")
	}
	if len(capacity.claims) != 0 {
		t.Fatalf("capacity claims = %v, want none", capacity.claims)
	}
	if len(executor.jobs) != 0 {
		t.Fatalf("executor jobs = %v, want none", executor.jobs)
	}
}

func TestDispatchPropagatesTheFrozenRoutePointerToJob(t *testing.T) {
	capacity := &fakeCapacity{}
	executor := &fakeExecutor{}
	broker := New(capacity, executor)
	route := &modelrouter.ResolvedRoute{
		Mode:            modelrouter.ModeNormal,
		Generation:      1,
		Role:            modelrouter.RoleWorker,
		Provider:        "openai",
		Model:           "gpt-6-luna",
		WireAPI:         modelrouter.WireAPIResponses,
		ReasoningEffort: "high",
		BaseURL:         "https://api.openai.com/v1",
		SecretName:      "openai-model-gateway",
		SecretKey:       "api-key",
	}

	_, err := broker.Dispatch(context.Background(), Request{
		TaskID:    "task-1",
		AttemptID: "attempt-1",
		Route:     route,
	})
	if err != nil {
		t.Fatalf("Dispatch() error = %v", err)
	}
	if len(executor.jobs) != 1 {
		t.Fatalf("executor jobs = %d, want 1", len(executor.jobs))
	}
	if executor.jobs[0].Route != route {
		t.Fatalf("job route pointer = %p, want request route pointer %p", executor.jobs[0].Route, route)
	}
}
func (f *fakeExecutor) SendMessage(_ context.Context, id, msg string) error {
	f.messages = append(f.messages, id+":"+msg)
	return nil
}
func (f *fakeExecutor) Cancel(_ context.Context, id string) error {
	f.cancelled = append(f.cancelled, id)
	return nil
}
func (f *fakeExecutor) CollectResult(context.Context, string) (k3s.Result, error) {
	return f.result, nil
}

func TestDispatchKeepsClaimAndReusesJobForFollowUp(t *testing.T) {
	c := &fakeCapacity{}
	e := &fakeExecutor{result: k3s.Result{CommitSHA: "abc"}}
	b := New(c, e)
	d, err := b.Dispatch(context.Background(), Request{TaskID: "task-1", AttemptID: "attempt-1", Route: normalWorkerRoute(), Prompt: "make change"})
	if err != nil {
		t.Fatal(err)
	}
	if err = b.SendMessage(context.Background(), d, "follow up"); err != nil {
		t.Fatal(err)
	}
	if d.Job.ID != "job-1" || len(c.claims) != 1 || len(e.messages) != 1 || e.messages[0] != "job-1:follow up" {
		t.Fatalf("dispatch=%+v claims=%v messages=%v", d, c.claims, e.messages)
	}
}
func TestDispatchDoesNotReleaseClaimOnUnknownCreate(t *testing.T) {
	c := &fakeCapacity{}
	e := &fakeExecutor{createErr: k3s.ErrUnknown}
	b := New(c, e)
	_, err := b.Dispatch(context.Background(), Request{TaskID: "task-1", AttemptID: "attempt-1", Route: normalWorkerRoute()})
	if !errors.Is(err, k3s.ErrUnknown) {
		t.Fatalf("Dispatch() error=%v", err)
	}
	if len(c.released) != 0 {
		t.Fatalf("released=%v", c.released)
	}
}
func TestCancelReleasesClaimAfterExecutorCancellation(t *testing.T) {
	c := &fakeCapacity{}
	e := &fakeExecutor{}
	b := New(c, e)
	d, err := b.Dispatch(context.Background(), Request{TaskID: "task-1", AttemptID: "attempt-1", Route: normalWorkerRoute()})
	if err != nil {
		t.Fatal(err)
	}
	if err = b.Cancel(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	if len(e.cancelled) != 1 || len(c.released) != 1 {
		t.Fatalf("cancelled=%v released=%v", e.cancelled, c.released)
	}
}
