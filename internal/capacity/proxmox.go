package capacity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

var ErrJoinDelegated = errors.New("k3s join is delegated to the executor")

type ProxmoxConfig struct {
	BaseURL string
	Node    string
	Token   string
	Range   VMIDRange
	Client  *http.Client
}

type ProxmoxRuntime struct {
	baseURL string
	node    string
	token   string
	range_  VMIDRange
	client  *http.Client
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
		range_:  config.Range,
		client:  client,
	}
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
		return nil, fmt.Errorf("proxmox %s %s returned %s", method, path, response.Status)
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
	if request.Storage != "" {
		form.Set("storage", request.Storage)
	}
	path := "/nodes/" + url.PathEscape(r.node) + "/lxc/" + strconv.Itoa(request.TemplateVMID) + "/clone"
	response, err := r.request(ctx, http.MethodPost, path, form)
	if err != nil {
		return Node{VMID: request.VMID, Generation: request.Generation, TaskID: request.TaskID, State: NodeUnknown}, ErrUnknown
	}
	_ = response.Body.Close()
	return Node{VMID: request.VMID, Generation: request.Generation, TaskID: request.TaskID, State: NodeCreating}, nil
}

func (r *ProxmoxRuntime) Observe(ctx context.Context, vmid int) (Node, error) {
	if err := r.validate(vmid); err != nil {
		return Node{}, err
	}
	path := "/nodes/" + url.PathEscape(r.node) + "/lxc/" + strconv.Itoa(vmid) + "/status/current"
	response, err := r.request(ctx, http.MethodGet, path, nil)
	if err != nil {
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

func (r *ProxmoxRuntime) action(ctx context.Context, method string, vmid int, suffix string) error {
	if err := r.validate(vmid); err != nil {
		return err
	}
	path := "/nodes/" + url.PathEscape(r.node) + "/lxc/" + strconv.Itoa(vmid) + suffix
	response, err := r.request(ctx, method, path, nil)
	if err != nil {
		return ErrUnknown
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
	return r.action(ctx, http.MethodPost, vmid, "/status/stop")
}

func (r *ProxmoxRuntime) Destroy(ctx context.Context, vmid int) error {
	return r.action(ctx, http.MethodDelete, vmid, "")
}
