package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/WilliamLi0623/codex-homelab/internal/domain"
	"github.com/WilliamLi0623/codex-homelab/internal/executor/k3s"
	"github.com/WilliamLi0623/codex-homelab/internal/orchestrator"
	"github.com/WilliamLi0623/codex-homelab/internal/store"
)

// ControllerClient adapts the existing Controller HTTP API to the MCP
// transport-neutral dispatcher/message-sender interfaces. It contains no task
// state of its own; the Controller remains the scheduler and source of truth.
type ControllerClient struct {
	baseURL    *url.URL
	token      string
	httpClient *http.Client
	store      *store.Store
}

func NewControllerClient(rawURL, token string, database *store.Store) (*ControllerClient, error) {
	parsed, err := url.Parse(strings.TrimRight(strings.TrimSpace(rawURL), "/"))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, fmt.Errorf("controller URL must be an HTTP(S) URL")
	}
	if database == nil {
		return nil, fmt.Errorf("controller client requires the authoritative store")
	}
	return &ControllerClient{baseURL: parsed, token: strings.TrimSpace(token), httpClient: http.DefaultClient, store: database}, nil
}

func (c *ControllerClient) StartAttempt(ctx context.Context, taskID, profile string) (domain.Task, domain.Attempt, error) {
	var response controllerAttemptResponse
	if err := c.postJSON(ctx, "/v1/tasks/"+url.PathEscape(taskID)+"/attempts", map[string]string{"profile": profile}, &response); err != nil {
		return domain.Task{}, domain.Attempt{}, err
	}
	return response.domainValues(taskID)
}

func (c *ControllerClient) RetryTask(ctx context.Context, taskID string) (domain.Task, domain.Attempt, error) {
	var response controllerAttemptResponse
	if err := c.postJSON(ctx, "/v1/tasks/"+url.PathEscape(taskID)+"/retry", struct{}{}, &response); err != nil {
		return domain.Task{}, domain.Attempt{}, err
	}
	return response.domainValues(taskID)
}

func (c *ControllerClient) Dispatch(ctx context.Context, request orchestrator.Request) (orchestrator.Dispatch, error) {
	var response struct {
		TaskID    string `json:"task_id"`
		AttemptID string `json:"attempt_id"`
		ClaimID   string `json:"claim_id"`
		VMID      int    `json:"vmid"`
		JobID     string `json:"job_id"`
		State     string `json:"state"`
	}
	if err := c.postJSON(ctx, "/v1/tasks/"+url.PathEscape(request.TaskID)+"/dispatch", map[string]any{
		"attempt_id":         request.AttemptID,
		"prompt":             request.Prompt,
		"validation_command": request.ValidationCommand,
	}, &response); err != nil {
		return orchestrator.Dispatch{}, err
	}
	if response.JobID == "" || response.TaskID != request.TaskID || response.AttemptID != request.AttemptID {
		return orchestrator.Dispatch{}, fmt.Errorf("controller returned an invalid dispatch response")
	}
	return orchestrator.Dispatch{
		Claim: orchestrator.Claim{ID: response.ClaimID, VMID: response.VMID, TaskID: response.TaskID, AttemptID: response.AttemptID},
		Job:   k3s.Job{ID: response.JobID, TaskID: response.TaskID, AttemptID: response.AttemptID, State: k3s.JobState(response.State)},
	}, nil
}

func (c *ControllerClient) SendMessageForAttempt(ctx context.Context, attemptID, body string) error {
	taskID, err := c.findTaskForAttempt(ctx, attemptID)
	if err != nil {
		return err
	}
	var response struct {
		State        string `json:"state"`
		ErrorSummary string `json:"error_summary"`
	}
	if err := c.postJSON(ctx, "/v1/tasks/"+url.PathEscape(taskID)+"/turns", map[string]any{
		"attempt_id":      attemptID,
		"body":            body,
		"idempotency_key": newContinuationKey(attemptID),
	}, &response); err != nil {
		return err
	}
	switch response.State {
	case string(store.ContinuationDelivered):
		return nil
	case string(store.ContinuationRejected):
		return store.ErrExecutionHandleNotFound
	case string(store.ContinuationUnknown):
		return fmt.Errorf("%w: %s", store.ErrContinuationDeliveryUnknown, response.ErrorSummary)
	default:
		return fmt.Errorf("controller returned unknown continuation state %q", response.State)
	}
}

func (c *ControllerClient) findTaskForAttempt(ctx context.Context, attemptID string) (string, error) {
	tasks, err := c.store.ListTasks(ctx)
	if err != nil {
		return "", fmt.Errorf("list tasks for attempt: %w", err)
	}
	for _, task := range tasks {
		if _, err := c.store.GetAttempt(ctx, task.ID, attemptID); err == nil {
			return task.ID, nil
		} else if !errors.Is(err, store.ErrAttemptNotFound) {
			return "", fmt.Errorf("find attempt owner: %w", err)
		}
	}
	return "", store.ErrAttemptNotFound
}

func (c *ControllerClient) postJSON(ctx context.Context, path string, input, output any) error {
	body, err := json.Marshal(input)
	if err != nil {
		return fmt.Errorf("encode Controller request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL.String()+path, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create Controller request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	if c.token != "" {
		request.Header.Set("Authorization", "Bearer "+c.token)
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("Controller request failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("Controller request returned HTTP %d", response.StatusCode)
	}
	limited := io.LimitReader(response.Body, 1<<20)
	if err := json.NewDecoder(limited).Decode(output); err != nil {
		return fmt.Errorf("decode Controller response: %w", err)
	}
	return nil
}

func newContinuationKey(attemptID string) string {
	return fmt.Sprintf("mcp-continuation-%s-%d", attemptID, time.Now().UnixNano())
}

type controllerAttemptResponse struct {
	Task struct {
		ID             string `json:"id"`
		Repository     string `json:"repository"`
		BaseRef        string `json:"base_ref"`
		Objective      string `json:"objective"`
		ExecutionClass string `json:"execution_class"`
		State          string `json:"state"`
	} `json:"task"`
	Attempt struct {
		ID           string `json:"id"`
		Number       int    `json:"number"`
		ModelProfile string `json:"model_profile"`
		State        string `json:"state"`
	} `json:"attempt"`
}

func (r controllerAttemptResponse) domainValues(expectedTaskID string) (domain.Task, domain.Attempt, error) {
	if r.Task.ID != expectedTaskID || r.Attempt.ID == "" {
		return domain.Task{}, domain.Attempt{}, fmt.Errorf("controller returned an invalid attempt response")
	}
	return domain.Task{
		ID: r.Task.ID, Repository: r.Task.Repository, BaseRef: r.Task.BaseRef, Objective: r.Task.Objective,
		ExecutionClass: domain.ExecutionClass(r.Task.ExecutionClass), State: domain.TaskState(r.Task.State),
	}, domain.Attempt{
		ID: r.Attempt.ID, TaskID: r.Task.ID, Number: r.Attempt.Number, ModelProfile: r.Attempt.ModelProfile,
		State: domain.AttemptState(r.Attempt.State),
	}, nil
}
