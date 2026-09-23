package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/WilliamLi0623/codex-homelab/internal/executor/k3s"
	"github.com/WilliamLi0623/codex-homelab/internal/store"
)

type ObservationReport struct {
	Inspected int
	NotReady  int
	Completed int
	Failed    int
	Unknown   int
	Errors    []error
}

// ObservationLoop polls durable dispatch specifications. It is deliberately
// a separate component so the Controller can start it after dependencies are
// assembled and tests can run one bounded pass without sleeping.
type ObservationLoop struct {
	store    *store.Store
	consumer *ResultConsumer
}

func NewObservationLoop(database *store.Store, consumer *ResultConsumer) *ObservationLoop {
	return &ObservationLoop{store: database, consumer: consumer}
}

func (l *ObservationLoop) RunOnce(ctx context.Context) (ObservationReport, error) {
	if l == nil || l.store == nil || l.consumer == nil {
		return ObservationReport{}, ErrObservationUnavailable
	}
	specs, err := l.store.ListPendingAttemptExecutionSpecs(ctx)
	if err != nil {
		return ObservationReport{}, err
	}
	report := ObservationReport{Inspected: len(specs)}
	for _, spec := range specs {
		_, _, err := l.consumer.ObserveAndCompleteStored(ctx, spec.TaskID, spec.AttemptID)
		switch {
		case err == nil:
			report.Completed++
		case errors.Is(err, ErrCompletionNotReady):
			report.NotReady++
		case errors.Is(err, ErrWorkerFailed), errors.Is(err, ErrWorkerCancelled):
			report.Failed++
			report.Errors = append(report.Errors, fmt.Errorf("attempt %s: %w", spec.AttemptID, err))
		case errors.Is(err, k3s.ErrUnknown), errors.Is(err, k3s.ErrUnknownUnresolved):
			report.Unknown++
			report.Errors = append(report.Errors, fmt.Errorf("attempt %s: %w", spec.AttemptID, err))
		default:
			report.Errors = append(report.Errors, fmt.Errorf("attempt %s: %w", spec.AttemptID, err))
		}
		if err := ctx.Err(); err != nil {
			return report, err
		}
	}
	return report, nil
}

// Run keeps polling until cancellation. Per-attempt errors are returned in
// the report and do not stop unrelated attempts in the same pass.
func (l *ObservationLoop) Run(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		return errors.New("observation interval must be positive")
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := l.RunOnce(ctx); err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}
