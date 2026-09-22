package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/WilliamLi0623/codex-homelab/internal/domain"
)

func TestListTaskEventsAfterReturnsOnlyEventsAfterCursor(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "controller.sqlite"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })

	task := domain.NewTask("task-events", "owner/repository", "main", "inspect", "events-request")
	if _, _, err := database.CreateTask(context.Background(), task); err != nil {
		t.Fatalf("CreateTask() error = %v", err)
	}
	if _, err := database.AppendUserMessage(context.Background(), task.ID, "follow up"); err != nil {
		t.Fatalf("AppendUserMessage() error = %v", err)
	}
	all, err := database.ListTaskEvents(context.Background(), task.ID)
	if err != nil || len(all) != 2 {
		t.Fatalf("ListTaskEvents() = %d, %v; want two events", len(all), err)
	}

	after, err := database.ListTaskEventsAfter(context.Background(), task.ID, all[0].ID)
	if err != nil {
		t.Fatalf("ListTaskEventsAfter() error = %v", err)
	}
	if len(after) != 1 || after[0].ID != all[1].ID {
		t.Fatalf("events after cursor = %+v, want only %q", after, all[1].ID)
	}
}

func TestListTaskEventsAfterRejectsUnknownCursor(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "controller.sqlite"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })

	task := domain.NewTask("task-events", "owner/repository", "main", "inspect", "events-request")
	if _, _, err := database.CreateTask(context.Background(), task); err != nil {
		t.Fatalf("CreateTask() error = %v", err)
	}
	_, err = database.ListTaskEventsAfter(context.Background(), task.ID, "event-does-not-exist")
	if !errors.Is(err, ErrTaskEventCursorNotFound) {
		t.Fatalf("ListTaskEventsAfter() error = %v, want ErrTaskEventCursorNotFound", err)
	}
}
