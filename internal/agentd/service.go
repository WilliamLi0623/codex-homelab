package agentd

import (
	"context"
	"errors"
	"fmt"

	"github.com/WilliamLi0623/codex-homelab/internal/store"
)

type Result struct {
	ThreadID string
	TurnID   string
	Events   []Event
}

// Service binds the transient App Server protocol to the Controller's durable
// Attempt-to-thread mapping. A retry must use a new Attempt, while follow-ups
// on one Attempt always resume its recorded thread.
type Service struct {
	client *Client
	store  *store.Store
}

func NewService(client *Client, database *store.Store) *Service {
	return &Service{client: client, store: database}
}

func (s *Service) RunAttempt(ctx context.Context, attemptID, prompt string) (Result, error) {
	if err := s.client.Initialize(ctx); err != nil {
		return Result{}, err
	}
	thread, err := s.store.GetCodexThread(ctx, attemptID)
	threadID := ""
	if err == nil {
		threadID = thread.CodexThreadID
		if err := s.client.ResumeThread(ctx, threadID); err != nil {
			return Result{}, fmt.Errorf("resume Codex thread: %w", err)
		}
	} else if errors.Is(err, store.ErrCodexThreadNotFound) {
		threadID, err = s.client.StartThread(ctx)
		if err != nil {
			return Result{}, fmt.Errorf("start Codex thread: %w", err)
		}
		if _, _, err := s.store.RecordCodexThread(ctx, attemptID, threadID); err != nil {
			return Result{}, fmt.Errorf("persist Codex thread: %w", err)
		}
	} else {
		return Result{}, fmt.Errorf("load Codex thread: %w", err)
	}

	turnID, err := s.client.StartTurn(ctx, threadID, prompt)
	if err != nil {
		return Result{}, fmt.Errorf("start Codex turn: %w", err)
	}
	events, err := s.client.CollectTurnEvents(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("collect Codex turn events: %w", err)
	}
	for _, event := range events {
		if err := s.store.AppendAttemptEvent(ctx, attemptID, "codex.event", event.Method); err != nil {
			return Result{}, fmt.Errorf("persist Codex event: %w", err)
		}
	}
	return Result{ThreadID: threadID, TurnID: turnID, Events: events}, nil
}

func (s *Service) CancelAttempt(ctx context.Context, attemptID, turnID string) error {
	if err := s.client.Initialize(ctx); err != nil {
		return err
	}
	thread, err := s.store.GetCodexThread(ctx, attemptID)
	if err != nil {
		return fmt.Errorf("load Codex thread for cancellation: %w", err)
	}
	return s.client.InterruptTurn(ctx, thread.CodexThreadID, turnID)
}
