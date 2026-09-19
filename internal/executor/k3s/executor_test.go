package k3s

import (
	"context"
	"errors"
	"testing"

	"github.com/WilliamLi0623/codex-homelab/internal/store"
)

type fakeRuntime struct {
	created   []JobRequest
	jobs      map[string]Job
	results   map[string]Result
	createErr error
	messages  []string
	cancelled []string
}

func (f *fakeRuntime) CreateJob(_ context.Context, r JobRequest) (Job, error) {
	f.created = append(f.created, r)
	if f.createErr != nil {
		return Job{}, f.createErr
	}
	if f.jobs == nil {
		f.jobs = map[string]Job{}
	}
	j := Job{ID: "job-1", TaskID: r.TaskID, AttemptID: r.AttemptID, State: JobRunning}
	f.jobs[j.ID] = j
	return j, nil
}
func (f *fakeRuntime) Observe(_ context.Context, id string) (Job, error) { return f.jobs[id], nil }
func (f *fakeRuntime) SendMessage(_ context.Context, id, msg string) error {
	f.messages = append(f.messages, id+":"+msg)
	return nil
}
func (f *fakeRuntime) Cancel(_ context.Context, id string) error {
	f.cancelled = append(f.cancelled, id)
	j := f.jobs[id]
	j.State = JobCancelled
	f.jobs[id] = j
	return nil
}
func (f *fakeRuntime) CollectResult(_ context.Context, id string) (Result, error) {
	return f.results[id], nil
}

type fakeHandles struct {
	handles map[string]store.ExecutionHandle
}

func (f *fakeHandles) GetExecutionHandleByExternalID(_ context.Context, id string) (store.ExecutionHandle, error) {
	for _, h := range f.handles {
		if h.ExternalID == id {
			return h, nil
		}
	}
	return store.ExecutionHandle{}, store.ErrExecutionHandleNotFound
}
func (f *fakeHandles) GetExecutionHandle(_ context.Context, id string) (store.ExecutionHandle, error) {
	h, ok := f.handles[id]
	if !ok {
		return store.ExecutionHandle{}, store.ErrExecutionHandleNotFound
	}
	return h, nil
}
func (f *fakeHandles) RecordExecutionHandle(_ context.Context, attempt, executor, external, state string) (store.ExecutionHandle, bool, error) {
	if f.handles == nil {
		f.handles = map[string]store.ExecutionHandle{}
	}
	if h, ok := f.handles[attempt]; ok {
		return h, false, nil
	}
	h := store.ExecutionHandle{AttemptID: attempt, Executor: executor, ExternalID: external, State: state}
	f.handles[attempt] = h
	return h, true, nil
}
func (f *fakeHandles) UpdateExecutionHandleState(_ context.Context, external, state string) error {
	for id, h := range f.handles {
		if h.ExternalID == external {
			h.State = state
			f.handles[id] = h
			return nil
		}
	}
	return store.ErrExecutionHandleNotFound
}
func TestCreateJobIsIdempotentPerAttempt(t *testing.T) {
	r := &fakeRuntime{}
	e := New(r, &fakeHandles{})
	req := JobRequest{TaskID: "task-1", AttemptID: "attempt-1", Prompt: "run tests"}
	one, err := e.CreateJob(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	two, err := e.CreateJob(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if one.ID != two.ID || len(r.created) != 1 {
		t.Fatalf("one=%+v two=%+v creates=%d", one, two, len(r.created))
	}
}
func TestExecutorDelegatesMessageCancellationAndResult(t *testing.T) {
	r := &fakeRuntime{results: map[string]Result{"job-1": {Output: "ok", CommitSHA: "abc123"}}}
	e := New(r, &fakeHandles{})
	j, err := e.CreateJob(context.Background(), JobRequest{TaskID: "task-1", AttemptID: "attempt-1"})
	if err != nil {
		t.Fatal(err)
	}
	if err = e.SendMessage(context.Background(), j.ID, "follow up"); err != nil {
		t.Fatal(err)
	}
	got, err := e.CollectResult(context.Background(), j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.CommitSHA != "abc123" {
		t.Fatalf("result=%+v", got)
	}
	if err = e.Cancel(context.Background(), j.ID); err != nil {
		t.Fatal(err)
	}
	if len(r.messages) != 1 || len(r.cancelled) != 1 {
		t.Fatalf("messages=%v cancelled=%v", r.messages, r.cancelled)
	}
}
func TestUnknownCreateCannotReplayAndCanReconcile(t *testing.T) {
	r := &fakeRuntime{createErr: ErrUnknown}
	h := &fakeHandles{}
	e := New(r, h)
	req := JobRequest{TaskID: "task-1", AttemptID: "attempt-1"}
	if _, err := e.CreateJob(context.Background(), req); !errors.Is(err, ErrUnknown) {
		t.Fatalf("create=%v", err)
	}
	if _, err := e.CreateJob(context.Background(), req); !errors.Is(err, ErrUnknownUnresolved) {
		t.Fatalf("replay=%v", err)
	}
	h.handles[req.AttemptID] = store.ExecutionHandle{AttemptID: req.AttemptID, Executor: executorName, ExternalID: "job-1", State: string(HandleUnknown)}
	r.createErr = nil
	r.jobs = map[string]Job{"job-1": {ID: "job-1", AttemptID: req.AttemptID, State: JobRunning}}
	j, err := e.Reconcile(context.Background(), req.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	if j.State != JobRunning {
		t.Fatalf("job=%+v", j)
	}
}
func TestUnknownMutationIsRejected(t *testing.T) {
	h := &fakeHandles{handles: map[string]store.ExecutionHandle{"attempt-1": {AttemptID: "attempt-1", ExternalID: "job-1", State: string(HandleUnknown)}}}
	e := New(&fakeRuntime{}, h)
	if err := e.SendMessage(context.Background(), "job-1", "retry"); !errors.Is(err, ErrUnknownUnresolved) {
		t.Fatalf("err=%v", err)
	}
}
