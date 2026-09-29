package codexsession

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
)

const sessionUIServiceName = "codex-session-ui"

// AppServerThread is a sanitized subset of the App Server thread metadata.
// It intentionally does not expose rollout paths or conversation contents.
type AppServerThread struct {
	ID              string  `json:"id"`
	Name            *string `json:"name"`
	ModelProvider   string  `json:"modelProvider"`
	Model           string  `json:"model"`
	ReasoningEffort *string `json:"reasoningEffort"`
	ApprovalPolicy  string  `json:"approvalPolicy"`
	CWD             string  `json:"cwd"`
	Preview         string  `json:"preview"`
	CreatedAt       int64   `json:"createdAt"`
	UpdatedAt       int64   `json:"updatedAt"`
	ParentThreadID  *string `json:"parentThreadId"`
	ThreadSource    *string `json:"threadSource"`
	Ephemeral       bool    `json:"ephemeral"`
}

type ThreadStartOptions struct {
	CWD                   string
	ModelProvider         string
	Model                 string
	Effort                string
	Config                map[string]any
	DeveloperInstructions string
	Ephemeral             bool
}

type ThreadPage struct {
	Threads    []AppServerThread
	NextCursor string
}

type RawPage struct {
	Items      []json.RawMessage
	NextCursor string
}

type AppServerClient struct {
	protocol    *ProtocolClient
	initMu      sync.Mutex
	initialized bool
}

func NewAppServerClient(protocol *ProtocolClient) *AppServerClient {
	return &AppServerClient{protocol: protocol}
}

func (c *AppServerClient) Initialize(ctx context.Context) error {
	if c == nil || c.protocol == nil {
		return errors.New("App Server protocol client is required")
	}
	c.initMu.Lock()
	defer c.initMu.Unlock()
	if c.initialized {
		return nil
	}
	var result json.RawMessage
	if err := c.protocol.Call(ctx, "initialize", map[string]any{
		"clientInfo": map[string]string{
			"name":    "codex-session-ui",
			"title":   "Codex Local Session UI",
			"version": "0.1.0",
		},
	}, &result); err != nil {
		return err
	}
	if err := c.protocol.Notify(ctx, "initialized", map[string]any{}); err != nil {
		return err
	}
	c.initialized = true
	return nil
}

func (c *AppServerClient) StartThread(ctx context.Context, options ThreadStartOptions) (AppServerThread, error) {
	if options.CWD == "" || options.ModelProvider == "" || options.Model == "" || options.Effort == "" {
		return AppServerThread{}, errors.New("thread cwd, provider, model, and reasoning effort are required")
	}
	if err := c.Initialize(ctx); err != nil {
		return AppServerThread{}, err
	}
	config := make(map[string]any, len(options.Config)+2)
	for key, value := range options.Config {
		config[key] = value
	}
	config["model_reasoning_effort"] = options.Effort
	config["model_provider"] = options.ModelProvider
	params := map[string]any{
		"cwd":           options.CWD,
		"modelProvider": options.ModelProvider,
		"model":         options.Model,
		"config":        config,
		"ephemeral":     options.Ephemeral,
		"serviceName":   sessionUIServiceName,
		"threadSource":  sessionUIServiceName,
	}
	if options.DeveloperInstructions != "" {
		params["developerInstructions"] = options.DeveloperInstructions
	}
	var response struct {
		Thread          AppServerThread `json:"thread"`
		ModelProvider   string          `json:"modelProvider"`
		Model           string          `json:"model"`
		ReasoningEffort *string         `json:"reasoningEffort"`
		ApprovalPolicy  string          `json:"approvalPolicy"`
	}
	if err := c.protocol.Call(ctx, "thread/start", params, &response); err != nil {
		return AppServerThread{}, err
	}
	thread := response.Thread
	thread.ModelProvider = response.ModelProvider
	thread.Model = response.Model
	thread.ReasoningEffort = response.ReasoningEffort
	thread.ApprovalPolicy = response.ApprovalPolicy
	if thread.ID == "" || thread.ModelProvider != options.ModelProvider || thread.Model != options.Model || thread.ReasoningEffort == nil || *thread.ReasoningEffort != options.Effort {
		return AppServerThread{}, fmt.Errorf("App Server did not confirm requested provider/model/effort for new thread")
	}
	return thread, nil
}

func (c *AppServerClient) ResumeThread(ctx context.Context, threadID string) (AppServerThread, error) {
	if threadID == "" {
		return AppServerThread{}, errors.New("thread ID is required")
	}
	if err := c.Initialize(ctx); err != nil {
		return AppServerThread{}, err
	}
	var response struct {
		Thread          AppServerThread `json:"thread"`
		ModelProvider   string          `json:"modelProvider"`
		Model           string          `json:"model"`
		ReasoningEffort *string         `json:"reasoningEffort"`
		ApprovalPolicy  string          `json:"approvalPolicy"`
	}
	if err := c.protocol.Call(ctx, "thread/resume", map[string]any{"threadId": threadID, "excludeTurns": true}, &response); err != nil {
		return AppServerThread{}, err
	}
	thread := response.Thread
	thread.ModelProvider = response.ModelProvider
	thread.Model = response.Model
	thread.ReasoningEffort = response.ReasoningEffort
	thread.ApprovalPolicy = response.ApprovalPolicy
	if thread.ID != threadID || thread.ModelProvider == "" || thread.Model == "" {
		return AppServerThread{}, errors.New("App Server resume did not return the requested thread metadata")
	}
	return thread, nil
}

func (c *AppServerClient) ListThreads(ctx context.Context, limit int, cursor string) (ThreadPage, error) {
	if err := c.Initialize(ctx); err != nil {
		return ThreadPage{}, err
	}
	if limit < 1 || limit > 100 {
		return ThreadPage{}, errors.New("thread page size must be between 1 and 100")
	}
	params := map[string]any{"limit": limit, "sortDirection": "desc", "sourceKinds": []string{"appServer"}}
	if cursor != "" {
		params["cursor"] = cursor
	}
	var response struct {
		Data       []AppServerThread `json:"data"`
		NextCursor string            `json:"nextCursor"`
	}
	if err := c.protocol.Call(ctx, "thread/list", params, &response); err != nil {
		return ThreadPage{}, err
	}
	return ThreadPage{Threads: response.Data, NextCursor: response.NextCursor}, nil
}

func (c *AppServerClient) ListTurns(ctx context.Context, threadID string, limit int, cursor string) (RawPage, error) {
	if threadID == "" {
		return RawPage{}, errors.New("thread ID is required")
	}
	if err := c.Initialize(ctx); err != nil {
		return RawPage{}, err
	}
	if limit < 1 || limit > 100 {
		return RawPage{}, errors.New("turn page size must be between 1 and 100")
	}
	params := map[string]any{"threadId": threadID, "limit": limit, "sortDirection": "desc", "itemsView": "summary"}
	if cursor != "" {
		params["cursor"] = cursor
	}
	var response struct {
		Data       []json.RawMessage `json:"data"`
		NextCursor string            `json:"nextCursor"`
	}
	if err := c.protocol.Call(ctx, "thread/turns/list", params, &response); err != nil {
		return RawPage{}, err
	}
	return RawPage{Items: response.Data, NextCursor: response.NextCursor}, nil
}

func (c *AppServerClient) ListItems(ctx context.Context, threadID, turnID string, limit int, cursor string) (RawPage, error) {
	if threadID == "" {
		return RawPage{}, errors.New("thread ID is required")
	}
	if err := c.Initialize(ctx); err != nil {
		return RawPage{}, err
	}
	if limit < 1 || limit > 100 {
		return RawPage{}, errors.New("item page size must be between 1 and 100")
	}
	params := map[string]any{"threadId": threadID, "limit": limit, "sortDirection": "desc"}
	if turnID != "" {
		params["turnId"] = turnID
	}
	if cursor != "" {
		params["cursor"] = cursor
	}
	var response struct {
		Data       []json.RawMessage `json:"data"`
		NextCursor string            `json:"nextCursor"`
	}
	if err := c.protocol.Call(ctx, "thread/items/list", params, &response); err != nil {
		return RawPage{}, err
	}
	return RawPage{Items: response.Data, NextCursor: response.NextCursor}, nil
}

func (c *AppServerClient) StartTurn(ctx context.Context, threadID, text string) (Turn, error) {
	if threadID == "" || text == "" {
		return Turn{}, errors.New("thread ID and turn text are required")
	}
	if err := c.Initialize(ctx); err != nil {
		return Turn{}, err
	}
	var response struct {
		Turn Turn `json:"turn"`
	}
	params := map[string]any{
		"threadId": threadID,
		"input":    []map[string]string{{"type": "text", "text": text}},
	}
	if err := c.protocol.Call(ctx, "turn/start", params, &response); err != nil {
		return Turn{}, err
	}
	if response.Turn.ID == "" {
		return Turn{}, errors.New("App Server returned an empty turn ID")
	}
	return response.Turn, nil
}

func (c *AppServerClient) InterruptTurn(ctx context.Context, threadID, turnID string) error {
	if threadID == "" || turnID == "" {
		return errors.New("thread ID and turn ID are required")
	}
	if err := c.Initialize(ctx); err != nil {
		return err
	}
	var response json.RawMessage
	return c.protocol.Call(ctx, "turn/interrupt", map[string]string{"threadId": threadID, "turnId": turnID}, &response)
}

func (c *AppServerClient) Events() <-chan Event { return c.protocol.Events() }

func (c *AppServerClient) ServerRequests() <-chan ServerRequest { return c.protocol.ServerRequests() }

func (c *AppServerClient) Respond(ctx context.Context, request ServerRequest, result any) error {
	return c.protocol.Respond(ctx, request.ID, result)
}

func (c *AppServerClient) Reject(ctx context.Context, request ServerRequest, code int, message string) error {
	return c.protocol.RespondError(ctx, request.ID, code, message)
}

type Turn struct {
	ID     string          `json:"id"`
	Status json.RawMessage `json:"status"`
}
