package orchestrator

import (
	"context"
	"errors"
	"fmt"

	"github.com/WilliamLi0623/codex-homelab/internal/executor/k3s"
	"github.com/WilliamLi0623/codex-homelab/internal/modelrouter"
)

type Claim struct {
	ID         string
	VMID       int
	TaskID     string
	AttemptID  string
	Generation string
	KubeNode   string
}
type ClaimRequest struct {
	TaskID    string
	AttemptID string
}
type Capacity interface {
	Create(context.Context, ClaimRequest) (Claim, error)
	Release(context.Context, Claim) error
}
type Request struct {
	TaskID            string
	AttemptID         string
	ModelProfile      string
	Route             *modelrouter.ResolvedRoute
	Prompt            string
	Repository        string
	BaseRef           string
	WorkspacePath     string
	ValidationCommand []string
}
type Dispatch struct {
	Claim Claim
	Job   k3s.Job
}
type Executor interface {
	CreateJob(context.Context, k3s.JobRequest) (k3s.Job, error)
	SendMessage(context.Context, string, string) error
	Cancel(context.Context, string) error
	CollectResult(context.Context, string) (k3s.Result, error)
}
type Broker struct {
	capacity Capacity
	executor Executor
}

func New(capacity Capacity, executor Executor) *Broker {
	return &Broker{capacity: capacity, executor: executor}
}
func (b *Broker) Dispatch(ctx context.Context, r Request) (Dispatch, error) {
	if r.TaskID == "" || r.AttemptID == "" {
		return Dispatch{}, fmt.Errorf("task and attempt IDs are required")
	}
	if r.Route == nil {
		return Dispatch{}, fmt.Errorf("frozen worker route is required")
	}
	if err := modelrouter.ValidateResolvedRoute(*r.Route); err != nil {
		return Dispatch{}, fmt.Errorf("invalid resolved route: %w", err)
	}
	if r.Route.Role != modelrouter.RoleWorker {
		return Dispatch{}, fmt.Errorf("invalid resolved route: role %q cannot be dispatched to a worker", r.Route.Role)
	}
	claim, err := b.capacity.Create(ctx, ClaimRequest{TaskID: r.TaskID, AttemptID: r.AttemptID})
	if err != nil {
		return Dispatch{}, err
	}
	job, err := b.executor.CreateJob(ctx, k3s.JobRequest{TaskID: r.TaskID, AttemptID: r.AttemptID, ModelProfile: r.ModelProfile, Route: r.Route, NodeName: claim.KubeNode, Prompt: r.Prompt, Repository: r.Repository, BaseRef: r.BaseRef, WorkspacePath: r.WorkspacePath, ValidationCommand: r.ValidationCommand})
	if errors.Is(err, k3s.ErrUnknown) {
		return Dispatch{Claim: claim}, err
	}
	if err != nil {
		_ = b.capacity.Release(ctx, claim)
		return Dispatch{}, err
	}
	return Dispatch{Claim: claim, Job: job}, nil
}
func (b *Broker) SendMessage(ctx context.Context, d Dispatch, message string) error {
	if d.Job.ID == "" {
		return fmt.Errorf("dispatch has no job")
	}
	return b.executor.SendMessage(ctx, d.Job.ID, message)
}
func (b *Broker) Cancel(ctx context.Context, d Dispatch) error {
	if d.Job.ID == "" {
		return fmt.Errorf("dispatch has no job")
	}
	if err := b.executor.Cancel(ctx, d.Job.ID); err != nil {
		return err
	}
	return b.capacity.Release(ctx, d.Claim)
}
func (b *Broker) CollectResult(ctx context.Context, d Dispatch) (k3s.Result, error) {
	if d.Job.ID == "" {
		return k3s.Result{}, fmt.Errorf("dispatch has no job")
	}
	return b.executor.CollectResult(ctx, d.Job.ID)
}
func (b *Broker) Complete(ctx context.Context, d Dispatch) (k3s.Result, error) {
	result, err := b.CollectResult(ctx, d)
	if err != nil {
		return k3s.Result{}, err
	}
	if err := b.capacity.Release(ctx, d.Claim); err != nil {
		return k3s.Result{}, err
	}
	return result, nil
}
