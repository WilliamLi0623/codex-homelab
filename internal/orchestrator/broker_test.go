package orchestrator

import (
	"context"
	"errors"
	"testing"

	"github.com/WilliamLi0623/codex-homelab/internal/executor/k3s"
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
	c := Claim{ID: r.AttemptID, VMID: 3010}
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
	d, err := b.Dispatch(context.Background(), Request{TaskID: "task-1", AttemptID: "attempt-1", Prompt: "make change"})
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
	_, err := b.Dispatch(context.Background(), Request{TaskID: "task-1", AttemptID: "attempt-1"})
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
	d, err := b.Dispatch(context.Background(), Request{TaskID: "task-1", AttemptID: "attempt-1"})
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
