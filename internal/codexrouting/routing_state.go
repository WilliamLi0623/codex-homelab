package codexrouting

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// RoutingState is the durable, public routing decision. It intentionally
// contains no provider credentials or model secret references.
type RoutingState struct {
	Mode       Mode
	ObservedAt time.Time
	Generation int64
}

// RoutingStateStore persists the last successfully published routing state.
// Implementations must make Save durable before returning nil.
type RoutingStateStore interface {
	Load(ctx context.Context) (state RoutingState, ok bool, err error)
	Save(ctx context.Context, state RoutingState) error
}

// ModeSink publishes a routing state to the controller.
type ModeSink interface {
	Publish(ctx context.Context, state RoutingState) error
}

func validateRoutingState(state RoutingState) error {
	if state.Mode != ModeNormal && state.Mode != ModeQuotaFallback {
		return fmt.Errorf("invalid routing mode %q", state.Mode)
	}
	if state.Generation <= 0 {
		return errors.New("routing generation must be positive")
	}
	if state.ObservedAt.IsZero() {
		return errors.New("routing observed_at is required")
	}
	return nil
}
