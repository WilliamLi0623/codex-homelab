package domain

import "fmt"

type ExecutionClass string

const (
	DedicatedLXC ExecutionClass = "dedicated-lxc"
	SharedLXC    ExecutionClass = "shared-lxc"
	VMSpecial    ExecutionClass = "vm-special"
)

type TaskState string

const (
	TaskReceived           TaskState = "RECEIVED"
	TaskPlanned            TaskState = "PLANNED"
	TaskWaitingForCapacity TaskState = "WAITING_FOR_CAPACITY"
	TaskQueued             TaskState = "QUEUED"
	TaskDispatched         TaskState = "DISPATCHED"
	TaskRunning            TaskState = "RUNNING"
	TaskValidating         TaskState = "VALIDATING"
	TaskPublishing         TaskState = "PUBLISHING"
	TaskSucceeded          TaskState = "SUCCEEDED"
	TaskFailed             TaskState = "FAILED"
	TaskBlocked            TaskState = "BLOCKED"
	TaskCancelled          TaskState = "CANCELLED"
)

type Task struct {
	ID             string
	Repository     string
	BaseRef        string
	Objective      string
	IdempotencyKey string
	ExecutionClass ExecutionClass
	State          TaskState
}

func NewTask(id, repository, baseRef, objective, idempotencyKey string) Task {
	return Task{
		ID:             id,
		Repository:     repository,
		BaseRef:        baseRef,
		Objective:      objective,
		IdempotencyKey: idempotencyKey,
		ExecutionClass: DedicatedLXC,
		State:          TaskReceived,
	}
}

func (t *Task) TransitionTo(next TaskState) error {
	if isTerminalTaskState(t.State) {
		return fmt.Errorf("task %s is terminal in state %s", t.ID, t.State)
	}
	if next == TaskFailed || next == TaskBlocked || next == TaskCancelled {
		t.State = next
		return nil
	}
	if validTaskTransition(t.State, next) {
		t.State = next
		return nil
	}
	return fmt.Errorf("invalid task transition: %s -> %s", t.State, next)
}

func validTaskTransition(current, next TaskState) bool {
	return (current == TaskReceived && next == TaskPlanned) ||
		(current == TaskPlanned && next == TaskWaitingForCapacity) ||
		(current == TaskWaitingForCapacity && next == TaskQueued) ||
		(current == TaskQueued && next == TaskDispatched) ||
		(current == TaskDispatched && next == TaskRunning) ||
		(current == TaskRunning && next == TaskValidating) ||
		(current == TaskValidating && next == TaskPublishing) ||
		(current == TaskPublishing && next == TaskSucceeded)
}

func isTerminalTaskState(state TaskState) bool {
	return state == TaskSucceeded || state == TaskFailed || state == TaskBlocked || state == TaskCancelled
}
