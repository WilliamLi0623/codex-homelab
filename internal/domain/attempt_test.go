package domain

import "testing"

func TestNewAttemptStartsInCreatedState(t *testing.T) {
	attempt := NewAttempt("attempt-1", "task-1", 1, "openai-primary")

	if attempt.State != AttemptCreated {
		t.Fatalf("State = %q, want %q", attempt.State, AttemptCreated)
	}
}

func TestAttemptTransitionRejectsProviderRetryAfterValidationFailure(t *testing.T) {
	attempt := NewAttempt("attempt-1", "task-1", 1, "openai-primary")
	for _, state := range []AttemptState{AttemptStarting, AttemptRunning, AttemptValidationFailed} {
		if err := attempt.TransitionTo(state); err != nil {
			t.Fatalf("TransitionTo(%q) returned %v", state, err)
		}
	}

	if err := attempt.TransitionTo(AttemptProviderFailed); err == nil {
		t.Fatal("TransitionTo(AttemptProviderFailed) succeeded after validation failure")
	}
}

func TestUnknownAttemptCannotTransitionWithoutReconciliation(t *testing.T) {
	attempt := NewAttempt("attempt-1", "task-1", 1, "openai-primary")
	attempt.State = AttemptUnknown

	if err := attempt.TransitionTo(AttemptStarting); err == nil {
		t.Fatal("TransitionTo(AttemptStarting) succeeded from UNKNOWN")
	}
}
