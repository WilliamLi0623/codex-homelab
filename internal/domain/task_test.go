package domain

import "testing"

func TestNewTaskDefaultsToDedicatedLXC(t *testing.T) {
	task := NewTask("task-1", "owner/repository", "main", "Fix the failing tests", "request-1")

	if task.ExecutionClass != DedicatedLXC {
		t.Fatalf("ExecutionClass = %q, want %q", task.ExecutionClass, DedicatedLXC)
	}
	if task.State != TaskReceived {
		t.Fatalf("State = %q, want %q", task.State, TaskReceived)
	}
}

func TestTaskTransitionRejectsSkippingCapacityPlanning(t *testing.T) {
	task := NewTask("task-1", "owner/repository", "main", "Fix the failing tests", "request-1")

	if err := task.TransitionTo(TaskRunning); err == nil {
		t.Fatal("TransitionTo(TaskRunning) succeeded, want state-transition error")
	}
	if task.State != TaskReceived {
		t.Fatalf("State changed to %q after rejected transition", task.State)
	}
}
