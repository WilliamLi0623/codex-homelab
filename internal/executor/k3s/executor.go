package k3s

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/WilliamLi0623/codex-homelab/internal/store"
)

const executorName = "k3s"
const unknownExternalPrefix = "unknown:"

var (
	ErrUnknown           = errors.New("external K3s operation outcome is unknown")
	ErrUnknownUnresolved = errors.New("K3s execution handle is UNKNOWN and must be reconciled")
)

type JobState string

const (
	JobPending   JobState = "PENDING"
	JobRunning   JobState = "RUNNING"
	JobSucceeded JobState = "SUCCEEDED"
	JobFailed    JobState = "FAILED"
	JobCancelled JobState = "CANCELLED"
	JobUnknown   JobState = "UNKNOWN"
)

type HandleState string

const (
	HandleCreated   HandleState = "CREATED"
	HandleRunning   HandleState = "RUNNING"
	HandleSucceeded HandleState = "SUCCEEDED"
	HandleFailed    HandleState = "FAILED"
	HandleCancelled HandleState = "CANCELLED"
	HandleUnknown   HandleState = "UNKNOWN"
)

type JobRequest struct {
	TaskID    string
	AttemptID string
	Prompt    string
}
type Job struct {
	ID        string
	TaskID    string
	AttemptID string
	State     JobState
}
type Result struct {
	Output    string
	CommitSHA string
}

type Runtime interface {
	CreateJob(context.Context, JobRequest) (Job, error)
	Observe(context.Context, string) (Job, error)
	SendMessage(context.Context, string, string) error
	Cancel(context.Context, string) error
	CollectResult(context.Context, string) (Result, error)
}
type HandleStore interface {
	GetExecutionHandle(context.Context, string) (store.ExecutionHandle, error)
	GetExecutionHandleByExternalID(context.Context, string) (store.ExecutionHandle, error)
	RecordExecutionHandle(context.Context, string, string, string, string) (store.ExecutionHandle, bool, error)
	UpdateExecutionHandleState(context.Context, string, string) error
}
type Executor struct {
	runtime Runtime
	handles HandleStore
	mu      sync.Mutex
}

func New(runtime Runtime, handles HandleStore) *Executor {
	return &Executor{runtime: runtime, handles: handles}
}

func (e *Executor) CreateJob(ctx context.Context, request JobRequest) (Job, error) {
	if request.AttemptID == "" || request.TaskID == "" {
		return Job{}, fmt.Errorf("task and attempt IDs are required")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if existing, err := e.handles.GetExecutionHandle(ctx, request.AttemptID); err == nil {
		if existing.State == string(HandleUnknown) {
			return Job{}, ErrUnknownUnresolved
		}
		return jobFromHandle(existing, request), nil
	} else if !errors.Is(err, store.ErrExecutionHandleNotFound) {
		return Job{}, err
	}
	job, err := e.runtime.CreateJob(ctx, request)
	if errors.Is(err, ErrUnknown) {
		external := job.ID
		if external == "" {
			external = unknownExternalPrefix + request.AttemptID
		}
		_, _, recordErr := e.handles.RecordExecutionHandle(ctx, request.AttemptID, executorName, external, string(HandleUnknown))
		if recordErr != nil {
			return Job{}, fmt.Errorf("record unknown K3s create: %w", recordErr)
		}
		return Job{}, ErrUnknown
	}
	if err != nil {
		return Job{}, err
	}
	if job.ID == "" {
		return Job{}, fmt.Errorf("K3s runtime returned an empty job ID")
	}
	if _, _, err := e.handles.RecordExecutionHandle(ctx, request.AttemptID, executorName, job.ID, handleState(job.State)); err != nil {
		return Job{}, fmt.Errorf("record K3s execution handle: %w", err)
	}
	return job, nil
}

func (e *Executor) Observe(ctx context.Context, attemptID string) (Job, error) {
	handle, err := e.handle(ctx, attemptID)
	if err != nil {
		return Job{}, err
	}
	return e.observeHandle(ctx, handle)
}
func (e *Executor) Reconcile(ctx context.Context, attemptID string) (Job, error) {
	handle, err := e.handles.GetExecutionHandle(ctx, attemptID)
	if err != nil {
		return Job{}, err
	}
	if strings.HasPrefix(handle.ExternalID, unknownExternalPrefix) {
		return Job{}, ErrUnknown
	}
	return e.observeHandle(ctx, handle)
}
func (e *Executor) SendMessage(ctx context.Context, jobID, message string) error {
	return e.mutate(ctx, jobID, func() error { return e.runtime.SendMessage(ctx, jobID, message) })
}
func (e *Executor) Cancel(ctx context.Context, jobID string) error {
	return e.mutate(ctx, jobID, func() error { return e.runtime.Cancel(ctx, jobID) })
}
func (e *Executor) CollectResult(ctx context.Context, jobID string) (Result, error) {
	handle, err := e.handles.GetExecutionHandleByExternalID(ctx, jobID)
	if err != nil {
		return Result{}, err
	}
	if handle.State == string(HandleUnknown) {
		return Result{}, ErrUnknownUnresolved
	}
	result, err := e.runtime.CollectResult(ctx, jobID)
	if errors.Is(err, ErrUnknown) {
		_ = e.handles.UpdateExecutionHandleState(ctx, jobID, string(HandleUnknown))
		return Result{}, ErrUnknown
	}
	if err != nil {
		return Result{}, err
	}
	if err := e.handles.UpdateExecutionHandleState(ctx, jobID, string(HandleSucceeded)); err != nil {
		return Result{}, err
	}
	return result, nil
}

func (e *Executor) handle(ctx context.Context, attemptID string) (store.ExecutionHandle, error) {
	handle, err := e.handles.GetExecutionHandle(ctx, attemptID)
	if err != nil {
		return store.ExecutionHandle{}, err
	}
	if handle.State == string(HandleUnknown) {
		return handle, ErrUnknownUnresolved
	}
	return handle, nil
}
func (e *Executor) observeHandle(ctx context.Context, handle store.ExecutionHandle) (Job, error) {
	if strings.HasPrefix(handle.ExternalID, unknownExternalPrefix) {
		return Job{}, ErrUnknown
	}
	job, err := e.runtime.Observe(ctx, handle.ExternalID)
	if errors.Is(err, ErrUnknown) {
		_ = e.handles.UpdateExecutionHandleState(ctx, handle.ExternalID, string(HandleUnknown))
		return Job{}, ErrUnknown
	}
	if err != nil {
		return Job{}, err
	}
	if err := e.handles.UpdateExecutionHandleState(ctx, handle.ExternalID, handleState(job.State)); err != nil {
		return Job{}, err
	}
	return job, nil
}
func (e *Executor) mutate(ctx context.Context, jobID string, operation func() error) error {
	handle, err := e.handles.GetExecutionHandleByExternalID(ctx, jobID)
	if err != nil {
		return err
	}
	if handle.State == string(HandleUnknown) {
		return ErrUnknownUnresolved
	}
	if err := operation(); errors.Is(err, ErrUnknown) {
		_ = e.handles.UpdateExecutionHandleState(ctx, jobID, string(HandleUnknown))
		return ErrUnknown
	} else {
		return err
	}
}
func jobFromHandle(handle store.ExecutionHandle, request JobRequest) Job {
	return Job{ID: handle.ExternalID, TaskID: request.TaskID, AttemptID: request.AttemptID, State: JobState(handle.State)}
}
func handleState(state JobState) string {
	switch state {
	case JobPending:
		return string(HandleCreated)
	case JobRunning:
		return string(HandleRunning)
	case JobSucceeded:
		return string(HandleSucceeded)
	case JobFailed:
		return string(HandleFailed)
	case JobCancelled:
		return string(HandleCancelled)
	default:
		return string(HandleUnknown)
	}
}
