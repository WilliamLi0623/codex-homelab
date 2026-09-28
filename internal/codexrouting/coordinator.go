package codexrouting

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"
)

// QuotaReader provides one authoritative quota observation.
type QuotaReader interface {
	Read(ctx context.Context) (QuotaSnapshot, error)
}

// Coordinator publishes only explicit normal/fallback classifications. Read
// failures and unknown classifications deliberately leave the current state
// untouched.
type Coordinator struct {
	Reader      QuotaReader
	Sink        ModeSink
	Store       RoutingStateStore
	Interval    time.Duration
	ReadTimeout time.Duration
	Now         func() time.Time
	OnError     func(error)

	pollGate chan struct{}
	loaded   bool
	current  RoutingState
	pending  bool
}

func NewCoordinator(reader QuotaReader, sink ModeSink, store RoutingStateStore, interval time.Duration) (*Coordinator, error) {
	if reader == nil || sink == nil || store == nil {
		return nil, errors.New("quota reader, mode sink, and routing-state store are required")
	}
	if interval <= 0 {
		return nil, errors.New("poll interval must be positive")
	}
	return &Coordinator{
		Reader: reader, Sink: sink, Store: store, Interval: interval, ReadTimeout: 15 * time.Second,
		Now: time.Now, pollGate: make(chan struct{}, 1),
	}, nil
}

// Run republishes durable state once, then polls until ctx is cancelled.
func (c *Coordinator) Run(ctx context.Context) error {
	// On startup, consult the current authoritative quota before publishing any
	// durable state. PollOnce republishes the saved generation only when the
	// fresh observation confirms that mode; a changed observation creates and
	// persists the next generation first.
	if err := c.PollOnce(ctx); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		c.report(err)
	}
	ticker := time.NewTicker(c.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := c.PollOnce(ctx); err != nil && ctx.Err() == nil && !errors.Is(err, context.Canceled) {
				// A failed observation or publication is recoverable. The next
				// tick retries without changing the durable state.
				c.report(err)
			}
		}
	}
}

func (c *Coordinator) report(err error) {
	if err != nil && c.OnError != nil {
		c.OnError(err)
	}
}

// PollOnce performs at most one quota read and one transition publication.
func (c *Coordinator) PollOnce(ctx context.Context) error {
	_, err := c.refreshAndDecide(ctx)
	return err
}

// RefreshAndDecide performs one quota read and returns the route decision a
// caller may use for a new session. A quota read failure is represented as an
// unknown, stale observation; operational failures such as persistence or
// Controller publication errors are returned and never yield a usable route.
func (c *Coordinator) RefreshAndDecide(ctx context.Context) (RouteDecision, error) {
	decision, err := c.refreshAndDecide(ctx)
	var observationErr *quotaObservationError
	if errors.As(err, &observationErr) {
		if ctx.Err() != nil {
			return RouteDecision{}, ctx.Err()
		}
		c.report(err)
		return decision, nil
	}
	return decision, err
}

func (c *Coordinator) refreshAndDecide(ctx context.Context) (RouteDecision, error) {
	if err := c.acquire(ctx); err != nil {
		return RouteDecision{}, err
	}
	defer c.release()
	if err := c.loadLocked(ctx); err != nil {
		return RouteDecision{}, err
	}
	readCtx, cancel := context.WithTimeout(ctx, c.ReadTimeout)
	defer cancel()
	snapshot, err := c.Reader.Read(readCtx)
	if err != nil {
		return c.decisionLocked(ModeUnknown, false), &quotaObservationError{err: err}
	}
	mode := ClassifyQuota(snapshot)
	if mode != ModeNormal && mode != ModeQuotaFallback {
		return c.decisionLocked(ModeUnknown, false), nil
	}
	if c.loaded && c.current.Mode == mode {
		if !c.pending {
			return c.decisionLocked(mode, true), nil
		}
		if err := c.Sink.Publish(ctx, c.current); err != nil {
			return c.decisionLocked(mode, true), err
		}
		c.pending = false
		return c.decisionLocked(mode, true), nil
	}
	generation := int64(1)
	if c.loaded {
		if c.current.Generation == math.MaxInt64 {
			return RouteDecision{}, errors.New("routing generation exhausted")
		}
		generation = c.current.Generation + 1
	}
	state := RoutingState{Mode: mode, ObservedAt: c.now(), Generation: generation}
	if err := validateRoutingState(state); err != nil {
		return RouteDecision{}, err
	}
	if err := c.Store.Save(ctx, state); err != nil {
		return RouteDecision{}, fmt.Errorf("persist routing state before publication: %w", err)
	}
	c.current, c.loaded, c.pending = state, true, true
	if err := c.Sink.Publish(ctx, state); err != nil {
		return c.decisionLocked(mode, true), err
	}
	c.pending = false
	return c.decisionLocked(mode, true), nil
}

func (c *Coordinator) decisionLocked(observed Mode, fresh bool) RouteDecision {
	if !c.loaded || (c.current.Mode != ModeNormal && c.current.Mode != ModeQuotaFallback) {
		return RouteDecision{Mode: ModeNormal, ObservedMode: observed, Fresh: fresh, CanStart: true}
	}
	return RouteDecision{
		Mode:         c.current.Mode,
		ObservedMode: observed,
		ObservedAt:   c.current.ObservedAt,
		Generation:   c.current.Generation,
		Fresh:        fresh,
		Published:    !c.pending,
		CanStart:     !c.pending,
	}
}

type quotaObservationError struct{ err error }

func (e *quotaObservationError) Error() string { return e.err.Error() }
func (e *quotaObservationError) Unwrap() error { return e.err }

func (c *Coordinator) acquire(ctx context.Context) error {
	select {
	case c.pollGate <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Coordinator) release() { <-c.pollGate }

func (c *Coordinator) loadLocked(ctx context.Context) error {
	if c.loaded {
		return nil
	}
	state, ok, err := c.Store.Load(ctx)
	if err != nil {
		return err
	}
	if ok {
		if err := validateRoutingState(state); err != nil {
			return fmt.Errorf("load routing state: %w", err)
		}
		c.current = state
		// Republish the existing generation after a successful fresh quota read.
		// The sink is generation-idempotent, so this also repairs uncertain prior
		// publication outcomes across process restarts.
		c.pending = true
	}
	c.loaded = true
	return nil
}

func (c *Coordinator) now() time.Time {
	if c.Now == nil {
		return time.Now()
	}
	return c.Now().UTC()
}
