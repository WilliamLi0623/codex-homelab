package domain

import "fmt"

type AttemptState string

const (
	AttemptCreated          AttemptState = "CREATED"
	AttemptStarting         AttemptState = "STARTING"
	AttemptRunning          AttemptState = "RUNNING"
	AttemptProviderFailed   AttemptState = "PROVIDER_FAILED"
	AttemptExecutionFailed  AttemptState = "EXECUTION_FAILED"
	AttemptValidationFailed AttemptState = "VALIDATION_FAILED"
	AttemptCompleted        AttemptState = "COMPLETED"
	AttemptCancelled        AttemptState = "CANCELLED"
	AttemptUnknown          AttemptState = "UNKNOWN"
)

type Attempt struct {
	ID           string
	TaskID       string
	Number       int
	ModelProfile string
	State        AttemptState
}

func NewAttempt(id, taskID string, number int, modelProfile string) Attempt {
	return Attempt{
		ID:           id,
		TaskID:       taskID,
		Number:       number,
		ModelProfile: modelProfile,
		State:        AttemptCreated,
	}
}

func (a *Attempt) TransitionTo(next AttemptState) error {
	if isTerminalAttemptState(a.State) || a.State == AttemptUnknown {
		return fmt.Errorf("attempt %s cannot transition from %s without reconciliation", a.ID, a.State)
	}
	if validAttemptTransition(a.State, next) {
		a.State = next
		return nil
	}
	return fmt.Errorf("invalid attempt transition: %s -> %s", a.State, next)
}

func validAttemptTransition(current, next AttemptState) bool {
	if current == AttemptCreated {
		return next == AttemptStarting || next == AttemptCancelled || next == AttemptUnknown
	}
	if current == AttemptStarting {
		return next == AttemptRunning || next == AttemptProviderFailed || next == AttemptExecutionFailed || next == AttemptCancelled || next == AttemptUnknown
	}
	if current == AttemptRunning {
		return next == AttemptProviderFailed || next == AttemptExecutionFailed || next == AttemptValidationFailed || next == AttemptCompleted || next == AttemptCancelled || next == AttemptUnknown
	}
	return false
}

func isTerminalAttemptState(state AttemptState) bool {
	return state == AttemptProviderFailed || state == AttemptExecutionFailed || state == AttemptValidationFailed || state == AttemptCompleted || state == AttemptCancelled
}
