package capacity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

var (
	ErrJoinDelegated = errors.New("k3s join is delegated to the executor")
	ErrRejected      = errors.New("proxmox operation rejected")
)

type ProxmoxConfig struct {
	BaseURL string
	Node    string
	Token   string
	Pool    string
	Range   VMIDRange
	Client  *http.Client
}

type ProxmoxRuntime struct {
	baseURL string
	node    string
	token   string
	pool    string
	range_  VMIDRange
	client  *http.Client
}

// ValidateConfig checks the configuration required by the Proxmox runtime.
// Callers should validate configuration before constructing a runtime.
func (config ProxmoxConfig) ValidateConfig() error {
	baseURL := strings.TrimSpace(config.BaseURL)
	parsedURL, err := url.Parse(baseURL)
	if err != nil || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") || parsedURL.Host == "" {
		return errors.New("proxmox BaseURL must be an http/https URL with a host")
	}
	if strings.TrimSpace(config.Node) == "" {
		return errors.New("proxmox node must not be empty")
	}
	if strings.TrimSpace(config.Token) == "" {
		return errors.New("proxmox token must not be empty")
	}
	if config.Range.Min > config.Range.Max || config.Range.Min > 3000 || config.Range.Max < 3999 {
		return errors.New("proxmox dynamic VMID range must be valid and cover 3000-3999")
	}
	return nil
}

func NewProxmoxRuntime(config ProxmoxConfig) *ProxmoxRuntime {
	client := config.Client
	if client == nil {
		client = http.DefaultClient
	}
	return &ProxmoxRuntime{
		baseURL: strings.TrimRight(config.BaseURL, "/"),
		node:    config.Node,
		token:   config.Token,
		pool:    defaultPool(config.Pool),
		range_:  config.Range,
		client:  client,
	}
}

func defaultPool(pool string) string {
	if strings.TrimSpace(pool) == "" {
		return "codex-workers"
	}
	return strings.TrimSpace(pool)
}

func (r *ProxmoxRuntime) validate(vmid int) error {
	if !r.range_.Contains(vmid) {
		return fmt.Errorf("%w: %d not in %d-%d", ErrVMIDOutsideRange, vmid, r.range_.Min, r.range_.Max)
	}
	return nil
}

func (r *ProxmoxRuntime) endpoint(path string) string {
	return r.baseURL + "/api2/json" + path
}

func (r *ProxmoxRuntime) request(ctx context.Context, method, path string, form url.Values) (*http.Response, error) {
	var body *strings.Reader
	if form == nil {
		body = strings.NewReader("")
	} else {
		body = strings.NewReader(form.Encode())
	}
	request, err := http.NewRequestWithContext(ctx, method, r.endpoint(path), body)
	if err != nil {
		return nil, err
	}
	if r.token != "" {
		request.Header.Set("Authorization", r.token)
	}
	if form != nil {
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	response, err := r.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("proxmox %s %s: %w", method, path, err)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		defer response.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		if response.StatusCode == http.StatusNotFound {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, strings.TrimSpace(string(body)))
		}
		if response.StatusCode >= http.StatusBadRequest && response.StatusCode < http.StatusInternalServerError {
			return nil, fmt.Errorf("%w: %s: %s", ErrRejected, response.Status, strings.TrimSpace(string(body)))
		}
		return nil, fmt.Errorf("proxmox %s %s returned %s: %s", method, path, response.Status, strings.TrimSpace(string(body)))
	}
	return response, nil
}

func (r *ProxmoxRuntime) Create(ctx context.Context, request CreateRequest) (Node, error) {
	if err := r.validate(request.VMID); err != nil {
		return Node{}, err
	}
	if err := r.validate(request.TemplateVMID); err != nil {
		return Node{}, fmt.Errorf("template vmid: %w", err)
	}
	form := url.Values{
		"newid": {strconv.Itoa(request.VMID)},
		"full":  {"1"},
	}
	if request.Hostname != "" {
		form.Set("hostname", request.Hostname)
	}
	if len(request.Metadata) > 0 {
		metadata, err := EncodeMetadata(request.Metadata)
		if err != nil {
			return Node{}, err
		}
		form.Set("description", metadata)
	}
	if request.Storage != "" {
		form.Set("storage", request.Storage)
	}
	if r.pool != "" {
		form.Set("pool", r.pool)
	}
	path := "/nodes/" + url.PathEscape(r.node) + "/lxc/" + strconv.Itoa(request.TemplateVMID) + "/clone"
	response, err := r.request(ctx, http.MethodPost, path, form)
	if err != nil {
		if isVMIDOccupiedError(err, request.VMID) {
			return Node{VMID: request.VMID, Generation: request.Generation, TaskID: request.TaskID, State: NodeStopped}, fmt.Errorf("%w: %v", ErrVMIDOccupied, err)
		}
		if errors.Is(err, ErrRejected) {
			return Node{VMID: request.VMID, Generation: request.Generation, TaskID: request.TaskID, State: NodeStopped}, err
		}
		return Node{VMID: request.VMID, Generation: request.Generation, TaskID: request.TaskID, State: NodeUnknown}, fmt.Errorf("%w: %v", ErrUnknown, err)
	}
	var envelope struct {
		Data string `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		_ = response.Body.Close()
		return Node{VMID: request.VMID, Generation: request.Generation, TaskID: request.TaskID, State: NodeUnknown}, fmt.Errorf("%w: decode clone task: %v", ErrUnknown, err)
	}
	_ = response.Body.Close()
	if envelope.Data == "" {
		return Node{VMID: request.VMID, Generation: request.Generation, TaskID: request.TaskID, State: NodeUnknown}, fmt.Errorf("%w: clone response omitted task id", ErrUnknown)
	}
	if err := r.waitForTask(ctx, envelope.Data); err != nil {
		return Node{VMID: request.VMID, Generation: request.Generation, TaskID: request.TaskID, State: NodeUnknown}, err
	}
	return Node{VMID: request.VMID, Generation: request.Generation, TaskID: request.TaskID, State: NodeCreating}, nil
}

func (r *ProxmoxRuntime) waitForTask(ctx context.Context, upid string) error {
	waitCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		path := "/nodes/" + url.PathEscape(r.node) + "/tasks/" + url.PathEscape(upid) + "/status"
		response, err := r.request(waitCtx, http.MethodGet, path, nil)
		if err == nil {
			var envelope struct {
				Data struct {
					Status     string `json:"status"`
					ExitStatus string `json:"exitstatus"`
				} `json:"data"`
			}
			decodeErr := json.NewDecoder(response.Body).Decode(&envelope)
			_ = response.Body.Close()
			if decodeErr != nil {
				return fmt.Errorf("%w: decode task status: %v", ErrUnknown, decodeErr)
			}
			if envelope.Data.Status == "stopped" {
				if envelope.Data.ExitStatus != "" && envelope.Data.ExitStatus != "OK" {
					return fmt.Errorf("%w: clone task %s exited with %s", ErrUnknown, upid, envelope.Data.ExitStatus)
				}
				return nil
			}
		} else if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("%w: observe clone task %s: %v", ErrUnknown, upid, err)
		}
		select {
		case <-waitCtx.Done():
			return fmt.Errorf("%w: wait for clone task %s: %v", ErrUnknown, upid, waitCtx.Err())
		case <-ticker.C:
		}
	}
}

func (r *ProxmoxRuntime) Observe(ctx context.Context, vmid int) (Node, error) {
	if err := r.validate(vmid); err != nil {
		return Node{}, err
	}
	path := "/nodes/" + url.PathEscape(r.node) + "/lxc/" + strconv.Itoa(vmid) + "/status/current"
	response, err := r.request(ctx, http.MethodGet, path, nil)
	if err != nil {
		if errors.Is(err, ErrNotFound) || isMissingVMIDError(err, vmid) {
			return Node{}, ErrNotFound
		}
		return Node{}, ErrUnknown
	}
	defer response.Body.Close()
	var envelope struct {
		Data struct {
			VMID   int    `json:"vmid"`
			Name   string `json:"name"`
			Status string `json:"status"`
		} `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		return Node{}, fmt.Errorf("decode Proxmox status: %w", err)
	}
	state := NodeUnknown
	switch envelope.Data.Status {
	case "running":
		state = NodeRunning
	case "stopped":
		state = NodeStopped
	}
	return Node{VMID: envelope.Data.VMID, KubeNode: envelope.Data.Name, State: state}, nil
}

// TargetAvailable uses the cluster inventory endpoint because Proxmox may
// report a missing per-guest status path as HTTP 500 with plain text.
func (r *ProxmoxRuntime) TargetAvailable(ctx context.Context, vmid int) (bool, error) {
	if err := r.validate(vmid); err != nil {
		return false, err
	}
	response, err := r.request(ctx, http.MethodGet, "/cluster/resources?type=vm", nil)
	if err != nil {
		return false, fmt.Errorf("observe Proxmox VM inventory: %w", err)
	}
	defer response.Body.Close()
	var envelope struct {
		Data []struct {
			VMID int
		}
	}
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		return false, fmt.Errorf("decode Proxmox VM inventory: %w", err)
	}
	for _, resource := range envelope.Data {
		if resource.VMID == vmid {
			return false, nil
		}
	}
	return true, nil
}

func isMissingVMIDError(err error, vmid int) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "configuration file") &&
		strings.Contains(message, fmt.Sprintf("lxc/%d.conf", vmid)) &&
		strings.Contains(message, "does not exist")
}

func isVMIDOccupiedError(err error, vmid int) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, fmt.Sprintf("ct %d already exists", vmid)) ||
		strings.Contains(message, fmt.Sprintf("vmid %d already exists", vmid))
}

// VerifyIdentity performs a read-only exact identity check before any release
// mutation. It verifies the dynamic VMID, deterministic hostname, and all
// ownership metadata stored in the LXC description.
func (r *ProxmoxRuntime) VerifyIdentity(ctx context.Context, node Node) error {
	if err := r.validate(node.VMID); err != nil {
		return err
	}
	if node.Generation == "" || node.TaskID == "" {
		return ErrIdentityInvalid
	}
	expectedHostname, err := DynamicHostname(node.VMID, node.Generation)
	if err != nil {
		return err
	}
	path := "/nodes/" + url.PathEscape(r.node) + "/lxc/" + strconv.Itoa(node.VMID) + "/config"
	response, err := r.request(ctx, http.MethodGet, path, nil)
	if err != nil {
		return ErrUnknown
	}
	defer response.Body.Close()
	var envelope struct {
		Data struct {
			VMID        int    `json:"vmid"`
			Hostname    string `json:"hostname"`
			Description string `json:"description"`
		} `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		return fmt.Errorf("decode Proxmox identity: %w", err)
	}
	if (envelope.Data.VMID != 0 && envelope.Data.VMID != node.VMID) || envelope.Data.Hostname != expectedHostname {
		return fmt.Errorf("%w: identity fields mismatch", ErrIdentityInvalid)
	}
	metadata, err := DecodeMetadata(envelope.Data.Description)
	if err != nil {
		return fmt.Errorf("%w: metadata decode failed", ErrIdentityInvalid)
	}
	if err := ValidateWorkerMetadata(metadata, node.Generation, node.TaskID); err != nil {
		return fmt.Errorf("%w: metadata validation failed", ErrIdentityInvalid)
	}
	return nil
}

func (r *ProxmoxRuntime) action(ctx context.Context, method string, vmid int, suffix string) error {
	if err := r.validate(vmid); err != nil {
		return err
	}
	path := "/nodes/" + url.PathEscape(r.node) + "/lxc/" + strconv.Itoa(vmid) + suffix
	response, err := r.request(ctx, method, path, nil)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnknown, err)
	}
	return response.Body.Close()
}

func (r *ProxmoxRuntime) Start(ctx context.Context, vmid int) error {
	return r.action(ctx, http.MethodPost, vmid, "/status/start")
}

func (r *ProxmoxRuntime) Join(context.Context, int) error {
	return ErrJoinDelegated
}

func (r *ProxmoxRuntime) Stop(ctx context.Context, vmid int) error {
	if err := r.validate(vmid); err != nil {
		return err
	}
	observed, err := r.Observe(ctx, vmid)
	if err != nil {
		return err
	}
	if observed.State == NodeStopped {
		return nil
	}
	return r.action(ctx, http.MethodPost, vmid, "/status/stop")
}

func (r *ProxmoxRuntime) Destroy(ctx context.Context, vmid int) error {
	return r.action(ctx, http.MethodDelete, vmid, "")
}
