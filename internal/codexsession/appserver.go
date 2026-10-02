package codexsession

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
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

// AppServerAccount is the non-identifying account metadata safe to return to
// callers. The account email is intentionally kept inside the read operation.
type AppServerAccount struct {
	Type               string
	PlanType           string
	RequiresOpenAIAuth bool
}

type appServerAccountRead struct {
	Account struct {
		Email    string `json:"email"`
		PlanType string `json:"planType"`
		Type     string `json:"type"`
	} `json:"account"`
	RequiresOpenAIAuth *bool `json:"requiresOpenaiAuth"`
}

// DeviceCodeChallenge is a short-lived, one-time login challenge. Callers may
// display it to the authenticated operator but must not persist or log it.
type DeviceCodeChallenge struct {
	LoginID         string
	VerificationURL string
	UserCode        string
}

func (DeviceCodeChallenge) String() string   { return "DeviceCodeChallenge{redacted}" }
func (DeviceCodeChallenge) GoString() string { return "DeviceCodeChallenge{redacted}" }

type deviceCodeLoginResponse struct {
	Type            string `json:"type"`
	LoginID         string `json:"loginId"`
	VerificationURL string `json:"verificationUrl"`
	UserCode        string `json:"userCode"`
}

// StartDeviceCodeLogin starts Codex's ChatGPT device-code flow for this
// App Server's own CODEX_HOME. The one-time user code is returned only to the
// caller and is never persisted by this client.
func (c *AppServerClient) StartDeviceCodeLogin(ctx context.Context) (DeviceCodeChallenge, error) {
	if err := c.Initialize(ctx); err != nil {
		return DeviceCodeChallenge{}, err
	}
	var response deviceCodeLoginResponse
	if err := c.protocol.Call(ctx, "account/login/start", map[string]string{"type": "chatgptDeviceCode"}, &response); err != nil {
		return DeviceCodeChallenge{}, err
	}
	if !validLoginID(response.LoginID) {
		return DeviceCodeChallenge{}, errors.New("App Server returned an invalid device login challenge")
	}
	if response.Type != "chatgptDeviceCode" {
		if _, err := c.cancelDeviceCodeLogin(ctx, response.LoginID); err != nil {
			return DeviceCodeChallenge{}, errors.New("unexpected device login response; cancellation outcome is unknown")
		}
		return DeviceCodeChallenge{}, errors.New("App Server returned an invalid device login challenge")
	}
	if !validDeviceCode(response.UserCode) || !trustedDeviceVerificationURL(response.VerificationURL) {
		if _, err := c.cancelDeviceCodeLogin(ctx, response.LoginID); err != nil {
			return DeviceCodeChallenge{}, errors.New("invalid device login challenge; cancellation outcome is unknown")
		}
		return DeviceCodeChallenge{}, errors.New("App Server returned an invalid device login challenge")
	}
	return DeviceCodeChallenge{
		LoginID:         response.LoginID,
		VerificationURL: response.VerificationURL,
		UserCode:        response.UserCode,
	}, nil
}

// CancelDeviceCodeLogin cancels one pending App Server device-code login.
// The bool is false when Codex reports that the login ID is no longer active.
func (c *AppServerClient) CancelDeviceCodeLogin(ctx context.Context, loginID string) (bool, error) {
	if !validLoginID(loginID) {
		return false, errors.New("App Server login ID is invalid")
	}
	if err := c.Initialize(ctx); err != nil {
		return false, err
	}
	return c.cancelDeviceCodeLogin(ctx, loginID)
}

func (c *AppServerClient) cancelDeviceCodeLogin(ctx context.Context, loginID string) (bool, error) {
	var response struct {
		Status string `json:"status"`
	}
	if err := c.protocol.Call(ctx, "account/login/cancel", map[string]string{"loginId": loginID}, &response); err != nil {
		return false, err
	}
	switch response.Status {
	case "canceled":
		return true, nil
	case "notFound":
		return false, nil
	default:
		return false, errors.New("App Server returned an invalid device login cancellation status")
	}
}

func validLoginID(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return false
	}
	for i := range value {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			continue
		}
		if !isHexByte(value[i]) {
			return false
		}
	}
	return true
}

func validDeviceCode(value string) bool {
	if value == "" || len(value) > 64 || strings.TrimSpace(value) != value {
		return false
	}
	for i := 0; i < len(value); i++ {
		ch := value[i]
		if (ch >= 'A' && ch <= 'Z') || (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || ch == '-' {
			continue
		}
		return false
	}
	return true
}

func isHexByte(value byte) bool {
	return (value >= '0' && value <= '9') || (value >= 'a' && value <= 'f') || (value >= 'A' && value <= 'F')
}

func trustedDeviceVerificationURL(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() != "auth.openai.com" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	if port := parsed.Port(); port != "" && port != "443" {
		return false
	}
	return parsed.Path == "/codex/device" || parsed.Path == "/codex/device/"
}

func (c *AppServerClient) readAccount(ctx context.Context) (AppServerAccount, string, error) {
	if err := c.Initialize(ctx); err != nil {
		return AppServerAccount{}, "", err
	}
	var response appServerAccountRead
	if err := c.protocol.Call(ctx, "account/read", map[string]bool{"refreshToken": false}, &response); err != nil {
		return AppServerAccount{}, "", err
	}
	if response.Account.Email == "" || response.Account.PlanType == "" || response.Account.Type == "" || response.RequiresOpenAIAuth == nil {
		return AppServerAccount{}, "", errors.New("App Server returned incomplete account metadata")
	}
	return AppServerAccount{
		Type:               response.Account.Type,
		PlanType:           response.Account.PlanType,
		RequiresOpenAIAuth: *response.RequiresOpenAIAuth,
	}, response.Account.Email, nil
}

// ReadAccount returns only non-identifying account metadata. It never returns
// or logs the email reported by the App Server.
func (c *AppServerClient) ReadAccount(ctx context.Context) (AppServerAccount, error) {
	account, _, err := c.readAccount(ctx)
	return account, err
}

// AccountEmailMatches compares the App Server's email in-process without
// returning the email to the caller. This is only a cached account-label
// comparison, not a stable identity or entitlement verification.
func (c *AppServerClient) AccountEmailMatches(ctx context.Context, expected string) (bool, error) {
	if strings.TrimSpace(expected) == "" {
		return false, errors.New("expected account email is required")
	}
	_, actual, err := c.readAccount(ctx)
	if err != nil {
		return false, err
	}
	return strings.EqualFold(strings.TrimSpace(actual), strings.TrimSpace(expected)), nil
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
