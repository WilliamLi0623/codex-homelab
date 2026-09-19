// Package mcp exposes the Controller's task operations as transport-neutral
// MCP tools. Protocol transports can list these tools and pass decoded tool
// arguments to CallTool without coupling the Controller to a specific client.
package mcp

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/WilliamLi0623/codex-homelab/internal/domain"
	"github.com/WilliamLi0623/codex-homelab/internal/store"
)

var (
	ErrInvalidArguments = errors.New("invalid tool arguments")
	ErrUnknownTool      = errors.New("unknown tool")
)

type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

type Task struct {
	ID             string `json:"id"`
	Repository     string `json:"repository"`
	BaseRef        string `json:"base_ref"`
	Objective      string `json:"objective"`
	ExecutionClass string `json:"execution_class"`
	State          string `json:"state"`
}

type Attempt struct {
	ID           string `json:"id"`
	Number       int    `json:"number"`
	ModelProfile string `json:"model_profile"`
	State        string `json:"state"`
}

type Message struct {
	ID        string `json:"id"`
	TaskID    string `json:"task_id"`
	Role      string `json:"role"`
	CreatedAt string `json:"created_at"`
}

type Event struct {
	ID        string `json:"id"`
	TaskID    string `json:"task_id"`
	Type      string `json:"type"`
	CreatedAt string `json:"created_at"`
	Payload   string `json:"-"`
}

type SubmitTaskResult struct {
	Task    Task `json:"task"`
	Created bool `json:"created"`
}

type GetTaskResult struct {
	Task Task `json:"task"`
}

type ListTasksResult struct {
	Tasks []Task `json:"tasks"`
}

type SendMessageResult struct {
	Message Message `json:"message"`
}

type CancelTaskResult struct {
	Task Task `json:"task"`
}

type RetryTaskResult struct {
	Task    Task    `json:"task"`
	Attempt Attempt `json:"attempt"`
}

type GetTaskEventsResult struct {
	Events []Event `json:"events"`
}

type Server struct {
	store *store.Store
}

func NewServer(database *store.Store) *Server {
	return &Server{store: database}
}

func (s *Server) Tools() []Tool {
	return []Tool{
		tool("submit_task", "Submit an idempotent Controller task. Normal tasks default to dedicated-lxc.", objectSchema(
			[]string{"repository", "base_ref", "objective", "idempotency_key"},
			map[string]any{"repository": stringSchema(), "base_ref": stringSchema(), "objective": stringSchema(), "idempotency_key": stringSchema()},
		)),
		tool("start_attempt", "Create the initial append-only Attempt for a task.", objectSchema([]string{"task_id"}, map[string]any{"task_id": stringSchema(), "profile": stringSchema()})),
		tool("get_task", "Get the current observable state of one task.", taskIDSchema()),
		tool("list_tasks", "List Controller tasks in creation order.", objectSchema(nil, map[string]any{})),
		tool("send_message", "Append a user follow-up message to an existing task.", objectSchema(
			[]string{"task_id", "body"}, map[string]any{"task_id": stringSchema(), "body": stringSchema()},
		)),
		tool("cancel_task", "Persist cancellation intent for a task.", taskIDSchema()),
		tool("retry_task", "Create a new append-only attempt. The Controller rejects unresolved UNKNOWN outcomes.", taskIDSchema()),
		tool("get_task_events", "List observable task events without hidden chain-of-thought.", taskIDSchema()),
	}
}

func (s *Server) CallTool(ctx context.Context, name string, arguments json.RawMessage) (any, error) {
	switch name {
	case "submit_task":
		var input submitTaskArguments
		if err := decodeArguments(arguments, &input); err != nil {
			return nil, err
		}
		if empty(input.Repository, input.BaseRef, input.Objective, input.IdempotencyKey) {
			return nil, fmt.Errorf("%w: repository, base_ref, objective, and idempotency_key are required", ErrInvalidArguments)
		}
		task := domain.NewTask(newTaskID(), input.Repository, input.BaseRef, input.Objective, input.IdempotencyKey)
		persisted, created, err := s.store.CreateTask(ctx, task)
		if err != nil {
			return nil, err
		}
		return SubmitTaskResult{Task: taskView(persisted), Created: created}, nil

	case "start_attempt":
		var input startAttemptArguments
		if err := decodeArguments(arguments, &input); err != nil {
			return nil, err
		}
		if empty(input.TaskID) {
			return nil, fmt.Errorf("%w: task_id is required", ErrInvalidArguments)
		}
		profile := input.Profile
		if profile == "" {
			profile = "openai-primary"
		}
		task, attempt, err := s.store.StartAttempt(ctx, input.TaskID, profile)
		if err != nil {
			return nil, err
		}
		return RetryTaskResult{Task: taskView(task), Attempt: attemptView(attempt)}, nil
	case "get_task":
		var input taskArguments
		if err := decodeArguments(arguments, &input); err != nil {
			return nil, err
		}
		if empty(input.TaskID) {
			return nil, fmt.Errorf("%w: task_id is required", ErrInvalidArguments)
		}
		task, err := s.store.GetTask(ctx, input.TaskID)
		if err != nil {
			return nil, err
		}
		return GetTaskResult{Task: taskView(task)}, nil

	case "list_tasks":
		var input struct{}
		if err := decodeArguments(arguments, &input); err != nil {
			return nil, err
		}
		tasks, err := s.store.ListTasks(ctx)
		if err != nil {
			return nil, err
		}
		result := ListTasksResult{Tasks: make([]Task, 0, len(tasks))}
		for _, task := range tasks {
			result.Tasks = append(result.Tasks, taskView(task))
		}
		return result, nil

	case "send_message":
		var input sendMessageArguments
		if err := decodeArguments(arguments, &input); err != nil {
			return nil, err
		}
		if empty(input.TaskID, input.Body) {
			return nil, fmt.Errorf("%w: task_id and body are required", ErrInvalidArguments)
		}
		message, err := s.store.AppendUserMessage(ctx, input.TaskID, input.Body)
		if err != nil {
			return nil, err
		}
		return SendMessageResult{Message: Message{ID: message.ID, TaskID: message.TaskID, Role: message.Role, CreatedAt: message.CreatedAt.Format(time.RFC3339Nano)}}, nil

	case "cancel_task":
		var input taskArguments
		if err := decodeArguments(arguments, &input); err != nil {
			return nil, err
		}
		if empty(input.TaskID) {
			return nil, fmt.Errorf("%w: task_id is required", ErrInvalidArguments)
		}
		task, err := s.store.CancelTask(ctx, input.TaskID)
		if err != nil {
			return nil, err
		}
		return CancelTaskResult{Task: taskView(task)}, nil

	case "retry_task":
		var input taskArguments
		if err := decodeArguments(arguments, &input); err != nil {
			return nil, err
		}
		if empty(input.TaskID) {
			return nil, fmt.Errorf("%w: task_id is required", ErrInvalidArguments)
		}
		task, attempt, err := s.store.RetryTask(ctx, input.TaskID)
		if err != nil {
			return nil, err
		}
		return RetryTaskResult{Task: taskView(task), Attempt: attemptView(attempt)}, nil

	case "get_task_events":
		var input taskArguments
		if err := decodeArguments(arguments, &input); err != nil {
			return nil, err
		}
		if empty(input.TaskID) {
			return nil, fmt.Errorf("%w: task_id is required", ErrInvalidArguments)
		}
		if _, err := s.store.GetTask(ctx, input.TaskID); err != nil {
			return nil, err
		}
		events, err := s.store.ListTaskEvents(ctx, input.TaskID)
		if err != nil {
			return nil, err
		}
		result := GetTaskEventsResult{Events: make([]Event, 0, len(events))}
		for _, event := range events {
			result.Events = append(result.Events, Event{ID: event.ID, TaskID: event.TaskID, Type: event.Type, CreatedAt: event.CreatedAt.Format(time.RFC3339Nano)})
		}
		return result, nil

	default:
		return nil, fmt.Errorf("%w: %s", ErrUnknownTool, name)
	}
}

type submitTaskArguments struct {
	Repository     string `json:"repository"`
	BaseRef        string `json:"base_ref"`
	Objective      string `json:"objective"`
	IdempotencyKey string `json:"idempotency_key"`
}

type startAttemptArguments struct {
	TaskID  string `json:"task_id"`
	Profile string `json:"profile"`
}
type taskArguments struct {
	TaskID string `json:"task_id"`
}

type sendMessageArguments struct {
	TaskID string `json:"task_id"`
	Body   string `json:"body"`
}

func decodeArguments(arguments json.RawMessage, destination any) error {
	if len(bytes.TrimSpace(arguments)) == 0 {
		arguments = json.RawMessage(`{}`)
	}
	decoder := json.NewDecoder(bytes.NewReader(arguments))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidArguments, err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("%w: exactly one JSON object is required", ErrInvalidArguments)
	}
	return nil
}

func empty(values ...string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return true
		}
	}
	return false
}

func taskView(task domain.Task) Task {
	return Task{ID: task.ID, Repository: task.Repository, BaseRef: task.BaseRef, Objective: task.Objective, ExecutionClass: string(task.ExecutionClass), State: string(task.State)}
}

func attemptView(attempt domain.Attempt) Attempt {
	return Attempt{ID: attempt.ID, Number: attempt.Number, ModelProfile: attempt.ModelProfile, State: string(attempt.State)}
}

func newTaskID() string {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	return "task-" + hex.EncodeToString(value)
}

func tool(name, description string, schema map[string]any) Tool {
	return Tool{Name: name, Description: description, InputSchema: schema}
}

func taskIDSchema() map[string]any {
	return objectSchema([]string{"task_id"}, map[string]any{"task_id": stringSchema()})
}

func objectSchema(required []string, properties map[string]any) map[string]any {
	schema := map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func stringSchema() map[string]any {
	return map[string]any{"type": "string", "minLength": 1}
}
