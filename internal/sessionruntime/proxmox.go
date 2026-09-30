package sessionruntime

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/WilliamLi0623/codex-homelab/internal/store"
)

type ProxmoxConfig struct {
	BaseURL string
	Node    string
	Token   string
	Client  *http.Client
}

type ProxmoxRuntime struct {
	baseURL string
	node    string
	token   string
	client  *http.Client
}

type proxmoxHTTPError struct {
	StatusCode int
	Status     string
	Body       string
}

func (e *proxmoxHTTPError) Error() string {
	return fmt.Sprintf("Proxmox returned %s: %s", e.Status, e.Body)
}

func NewProxmoxRuntime(config ProxmoxConfig) (*ProxmoxRuntime, error) {
	baseURL := strings.TrimSpace(config.BaseURL)
	parsed, err := url.Parse(baseURL)
	localHTTP := false
	if err == nil && parsed.Scheme == "http" {
		host := parsed.Hostname()
		ip := net.ParseIP(host)
		localHTTP = strings.EqualFold(host, "localhost") || (ip != nil && ip.IsLoopback())
	}
	if err != nil || (parsed.Scheme != "https" && !localHTTP) || parsed.Host == "" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" || strings.TrimSpace(config.Node) != config.Node || config.Node == "" || strings.TrimSpace(config.Token) != config.Token || config.Token == "" {
		return nil, fmt.Errorf("%w: base URL, node, and API token are required", ErrInvalidConfig)
	}
	client := config.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &ProxmoxRuntime{baseURL: strings.TrimRight(baseURL, "/"), node: config.Node, token: config.Token, client: client}, nil
}

type sessionOwnership struct {
	Version    int    `json:"version"`
	SessionID  string `json:"session_id"`
	EpochID    string `json:"epoch_id"`
	Generation string `json:"generation"`
}

func (r *ProxmoxRuntime) TargetAvailable(ctx context.Context, vmid int) (bool, error) {
	if !validSessionVMID(vmid) {
		return false, fmt.Errorf("VMID %d outside interactive Session range", vmid)
	}
	var envelope struct {
		Data []struct {
			VMID int `json:"vmid"`
		} `json:"data"`
	}
	if err := r.request(ctx, http.MethodGet, "/cluster/resources?type=vm", nil, &envelope); err != nil {
		return false, fmt.Errorf("read Proxmox VM inventory: %w", err)
	}
	for _, resource := range envelope.Data {
		if resource.VMID == vmid {
			return false, nil
		}
	}
	return true, nil
}

func (r *ProxmoxRuntime) Clone(ctx context.Context, request RuntimeRequest) (string, error) {
	if !validSessionVMID(request.VMID) || request.TemplateVMID < 3900 || request.TemplateVMID > 3902 || request.SystemStorage != SessionSystemStorage || request.WorkspaceStorage != "pool" || request.WorkspaceSizeGiB < 1 || request.SessionID == "" || request.EpochID == "" || request.Generation == "" || request.Hostname == "" {
		return "", fmt.Errorf("%w: invalid Session clone request", ErrInvalidConfig)
	}
	var source struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	sourcePath := "/nodes/" + url.PathEscape(r.node) + "/lxc/" + strconv.Itoa(request.TemplateVMID) + "/config"
	if err := r.request(ctx, http.MethodGet, sourcePath, nil, &source); err != nil {
		return "", fmt.Errorf("verify Session clone source: %w", err)
	}
	var isTemplate int
	if raw, ok := source.Data["template"]; !ok || json.Unmarshal(raw, &isTemplate) != nil || isTemplate != 1 {
		return "", fmt.Errorf("configured Session clone source %d is not verified as a Proxmox template", request.TemplateVMID)
	}
	ownership, err := encodeOwnership(sessionOwnership{Version: 1, SessionID: request.SessionID, EpochID: request.EpochID, Generation: request.Generation})
	if err != nil {
		return "", err
	}
	form := url.Values{
		"newid":       {strconv.Itoa(request.VMID)},
		"full":        {"1"},
		"hostname":    {request.Hostname},
		"storage":     {request.SystemStorage},
		"description": {ownership},
	}
	upid, err := r.postTask(ctx, "/nodes/"+url.PathEscape(r.node)+"/lxc/"+strconv.Itoa(request.TemplateVMID)+"/clone", form)
	if err != nil {
		return "", err
	}
	if err := r.waitTask(ctx, upid); err != nil {
		return "", err
	}
	if err := r.configureNetworkAndWorkspace(ctx, request); err != nil {
		return "", fmt.Errorf("configure Session LXC network/workspace: %w", err)
	}
	return r.WorkspaceVolume(ctx, request.VMID)
}

func (r *ProxmoxRuntime) configureNetworkAndWorkspace(ctx context.Context, request RuntimeRequest) error {
	path := "/nodes/" + url.PathEscape(r.node) + "/lxc/" + strconv.Itoa(request.VMID) + "/config"
	var envelope struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	if err := r.request(ctx, http.MethodGet, path, nil, &envelope); err != nil {
		return err
	}
	form := url.Values{}
	found := false
	for key, raw := range envelope.Data {
		if !strings.HasPrefix(key, "net") {
			continue
		}
		var network string
		if err := json.Unmarshal(raw, &network); err != nil {
			return fmt.Errorf("decode %s: %w", key, err)
		}
		found = true
		form.Set(key, withDHCP(network))
	}
	if !found {
		return errors.New("cloned Session LXC has no network interface to configure")
	}
	if raw, exists := envelope.Data["mp0"]; exists && len(raw) > 0 && string(raw) != "null" {
		return errors.New("Session template already contains mp0; refusing to replace an unknown mount")
	}
	mount := fmt.Sprintf("%s:%d,mp=/workspace,backup=1", request.WorkspaceStorage, request.WorkspaceSizeGiB)
	if request.WorkspaceVolumeID != "" {
		if !strings.HasPrefix(request.WorkspaceVolumeID, request.WorkspaceStorage+":") {
			return errors.New("existing Session workspace volume is not on configured storage")
		}
		mount = request.WorkspaceVolumeID + ",mp=/workspace,backup=1"
	}
	form.Set("mp0", mount)
	if err := r.request(ctx, http.MethodPut, path, form, nil); err != nil {
		return err
	}
	attached, err := r.WorkspaceVolume(ctx, request.VMID)
	if err != nil {
		return err
	}
	if request.WorkspaceVolumeID != "" && attached != request.WorkspaceVolumeID {
		return errors.New("Proxmox attached a different workspace volume than requested")
	}
	return nil
}

func (r *ProxmoxRuntime) WorkspaceVolume(ctx context.Context, vmid int) (string, error) {
	config, err := r.containerConfig(ctx, vmid)
	if err != nil {
		return "", err
	}
	value, ok := config["mp0"]
	if !ok {
		return "", errors.New("Session workspace mount mp0 is missing")
	}
	volumeID := volumeIDFromMount(value)
	if !strings.HasPrefix(volumeID, "pool:") || !hasMountPoint(value, "/workspace") {
		return "", errors.New("Session workspace mount is not the expected pool:/workspace volume")
	}
	return volumeID, nil
}

func (r *ProxmoxRuntime) DetachWorkspace(ctx context.Context, vmid int, expectedVolumeID string) error {
	if !validSessionVMID(vmid) || !strings.HasPrefix(expectedVolumeID, "pool:") {
		return errors.New("invalid Session VMID or workspace volume identity")
	}
	path := "/nodes/" + url.PathEscape(r.node) + "/lxc/" + strconv.Itoa(vmid) + "/config"
	config, err := r.containerConfig(ctx, vmid)
	if err != nil {
		return err
	}
	if mount, exists := config["mp0"]; exists {
		if volumeIDFromMount(mount) != expectedVolumeID || !hasMountPoint(mount, "/workspace") {
			return errors.New("refusing to detach a mount that does not match the Session workspace record")
		}
		if err := r.request(ctx, http.MethodPut, path, url.Values{"delete": {"mp0"}}, nil); err != nil {
			return err
		}
		config, err = r.containerConfig(ctx, vmid)
		if err != nil {
			return err
		}
	}
	for key, value := range config {
		if strings.HasPrefix(key, "unused") && volumeIDFromMount(value) == expectedVolumeID {
			return nil
		}
	}
	return errors.New("workspace volume is not attached or preserved as an unused Proxmox volume")
}

func (r *ProxmoxRuntime) WorkspaceVolumeExists(ctx context.Context, volumeID string) (bool, error) {
	if !strings.HasPrefix(volumeID, "pool:") {
		return false, errors.New("workspace volume is outside the configured pool storage")
	}
	var envelope struct {
		Data []struct {
			VolID string `json:"volid"`
		} `json:"data"`
	}
	path := "/nodes/" + url.PathEscape(r.node) + "/storage/pool/content?content=rootdir"
	if err := r.request(ctx, http.MethodGet, path, nil, &envelope); err != nil {
		return false, err
	}
	for _, item := range envelope.Data {
		if item.VolID == volumeID {
			return true, nil
		}
	}
	return false, nil
}

func (r *ProxmoxRuntime) containerConfig(ctx context.Context, vmid int) (map[string]string, error) {
	if !validSessionVMID(vmid) {
		return nil, fmt.Errorf("VMID %d outside interactive Session range", vmid)
	}
	var envelope struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	path := "/nodes/" + url.PathEscape(r.node) + "/lxc/" + strconv.Itoa(vmid) + "/config"
	if err := r.request(ctx, http.MethodGet, path, nil, &envelope); err != nil {
		return nil, err
	}
	config := make(map[string]string, len(envelope.Data))
	for key, raw := range envelope.Data {
		var value string
		if err := json.Unmarshal(raw, &value); err == nil {
			config[key] = value
		}
	}
	return config, nil
}

func volumeIDFromMount(mount string) string {
	for _, option := range strings.Split(mount, ",") {
		if strings.HasPrefix(option, "volume=") {
			return strings.TrimPrefix(option, "volume=")
		}
	}
	parts := strings.SplitN(mount, ",", 2)
	return parts[0]
}

func hasMountPoint(mount, expected string) bool {
	for _, option := range strings.Split(mount, ",") {
		if strings.TrimSpace(option) == "mp="+expected {
			return true
		}
	}
	return false
}

func withDHCP(network string) string {
	parts := strings.Split(network, ",")
	foundIP := false
	for index, part := range parts {
		if strings.HasPrefix(part, "ip=") {
			parts[index] = "ip=dhcp"
			foundIP = true
		}
	}
	if !foundIP {
		parts = append(parts, "ip=dhcp")
	}
	return strings.Join(parts, ",")
}

func (r *ProxmoxRuntime) Start(ctx context.Context, vmid int) error {
	return r.action(ctx, vmid, "/status/start")
}

// CheckReady remains fail-closed until the selected epoch's guest bootstrap,
// identity adapter, and Codex App Server readiness have been verified. A PVE
// running status or root console probe does not establish those capabilities.
// This method is read-only so reconciliation cannot replay bootstrap effects.
func (r *ProxmoxRuntime) CheckReady(ctx context.Context, _ store.SessionRuntimeBinding) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return fmt.Errorf("%w: Session guest/Codex bootstrap is not configured", ErrRuntimeNotReady)
}

func (r *ProxmoxRuntime) Stop(ctx context.Context, vmid int) error {
	return r.action(ctx, vmid, "/status/stop")
}

func (r *ProxmoxRuntime) Delete(ctx context.Context, vmid int) error {
	if !validSessionVMID(vmid) {
		return fmt.Errorf("VMID %d outside interactive Session range", vmid)
	}
	upid, err := r.requestTask(ctx, http.MethodDelete, "/nodes/"+url.PathEscape(r.node)+"/lxc/"+strconv.Itoa(vmid), url.Values{
		"destroy-unreferenced-disks": {"0"},
		"purge":                      {"0"},
	})
	if err != nil {
		return err
	}
	return r.waitTask(ctx, upid)
}

func (r *ProxmoxRuntime) action(ctx context.Context, vmid int, suffix string) error {
	if !validSessionVMID(vmid) {
		return fmt.Errorf("VMID %d outside interactive Session range", vmid)
	}
	upid, err := r.postTask(ctx, "/nodes/"+url.PathEscape(r.node)+"/lxc/"+strconv.Itoa(vmid)+suffix, nil)
	if err != nil {
		return err
	}
	return r.waitTask(ctx, upid)
}

func (r *ProxmoxRuntime) Observe(ctx context.Context, vmid int) (RuntimeState, error) {
	if !validSessionVMID(vmid) {
		return RuntimeUnknown, fmt.Errorf("VMID %d outside interactive Session range", vmid)
	}
	path := "/nodes/" + url.PathEscape(r.node) + "/lxc/" + strconv.Itoa(vmid) + "/status/current"
	var envelope struct {
		Data struct {
			VMID   int    `json:"vmid"`
			Status string `json:"status"`
		} `json:"data"`
	}
	err := r.request(ctx, http.MethodGet, path, nil, &envelope)
	if err == nil {
		if envelope.Data.VMID != 0 && envelope.Data.VMID != vmid {
			return RuntimeUnknown, fmt.Errorf("Proxmox status VMID mismatch: got %d want %d", envelope.Data.VMID, vmid)
		}
		switch envelope.Data.Status {
		case "running":
			return RuntimeRunning, nil
		case "stopped":
			return RuntimeStopped, nil
		default:
			return RuntimeUnknown, fmt.Errorf("unknown Proxmox LXC state %q", envelope.Data.Status)
		}
	} else {
		var statusErr *proxmoxHTTPError
		if !errors.As(err, &statusErr) || (statusErr.StatusCode != http.StatusNotFound && statusErr.StatusCode != http.StatusInternalServerError) {
			return RuntimeUnknown, err
		}
	}
	available, inventoryErr := r.TargetAvailable(ctx, vmid)
	if inventoryErr != nil {
		return RuntimeUnknown, inventoryErr
	}
	if available {
		return RuntimeMissing, nil
	}
	return RuntimeUnknown, fmt.Errorf("Proxmox status request failed while VMID %d remains in inventory", vmid)
}

func (r *ProxmoxRuntime) VerifyIdentity(ctx context.Context, binding store.SessionRuntimeBinding) error {
	if !validSessionVMID(binding.VMID) {
		return errors.New("refusing Session identity check outside reserved VMID range")
	}
	path := "/nodes/" + url.PathEscape(r.node) + "/lxc/" + strconv.Itoa(binding.VMID) + "/config"
	var envelope struct {
		Data struct {
			VMID         int    `json:"vmid"`
			Hostname     string `json:"hostname"`
			Description  string `json:"description"`
			Unprivileged int    `json:"unprivileged"`
			RootFS       string `json:"rootfs"`
		} `json:"data"`
	}
	if err := r.request(ctx, http.MethodGet, path, nil, &envelope); err != nil {
		return err
	}
	var owner sessionOwnership
	if envelope.Data.VMID != 0 && envelope.Data.VMID != binding.VMID {
		return errors.New("Proxmox guest VMID does not match the Session reservation")
	}
	if err := decodeOwnership(envelope.Data.Description, &owner); err != nil {
		return err
	}
	if envelope.Data.Unprivileged != 0 {
		return errors.New("Session runtime must use privileged LXC mode")
	}
	if !strings.HasPrefix(volumeIDFromMount(envelope.Data.RootFS), SessionSystemStorage+":") {
		return errors.New("Session runtime root disk must be on local SSD storage")
	}
	if envelope.Data.Hostname != sessionHostname(binding.SessionID, binding.EpochID, binding.Generation) || owner.Version != 1 || owner.SessionID != binding.SessionID || owner.EpochID != binding.EpochID || owner.Generation != binding.Generation {
		return errors.New("Proxmox guest ownership metadata does not match the Session binding")
	}
	return nil
}

func (r *ProxmoxRuntime) postTask(ctx context.Context, path string, form url.Values) (string, error) {
	return r.requestTask(ctx, http.MethodPost, path, form)
}

func (r *ProxmoxRuntime) requestTask(ctx context.Context, method, path string, form url.Values) (string, error) {
	var envelope struct {
		Data string `json:"data"`
	}
	if err := r.request(ctx, method, path, form, &envelope); err != nil {
		return "", err
	}
	if envelope.Data == "" {
		return "", fmt.Errorf("%w: Proxmox task response omitted UPID", ErrOutcomeUnknown)
	}
	return envelope.Data, nil
}

func (r *ProxmoxRuntime) waitTask(ctx context.Context, upid string) error {
	waitCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		var envelope struct {
			Data struct {
				Status     string `json:"status"`
				ExitStatus string `json:"exitstatus"`
			} `json:"data"`
		}
		path := "/nodes/" + url.PathEscape(r.node) + "/tasks/" + url.PathEscape(upid) + "/status"
		if err := r.request(waitCtx, http.MethodGet, path, nil, &envelope); err == nil && envelope.Data.Status == "stopped" {
			if envelope.Data.ExitStatus != "" && envelope.Data.ExitStatus != "OK" {
				return fmt.Errorf("Proxmox task %s failed with %s", upid, envelope.Data.ExitStatus)
			}
			return nil
		}
		select {
		case <-waitCtx.Done():
			return fmt.Errorf("%w: waiting for Proxmox task completion: %v", ErrOutcomeUnknown, waitCtx.Err())
		case <-ticker.C:
		}
	}
}

func (r *ProxmoxRuntime) request(ctx context.Context, method, path string, form url.Values, output any) error {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	request, err := http.NewRequestWithContext(ctx, method, r.baseURL+"/api2/json"+path, body)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", r.token)
	if form != nil {
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	response, err := r.client.Do(request)
	if err != nil {
		if method != http.MethodGet {
			return fmt.Errorf("%w: Proxmox %s %s: %v", ErrOutcomeUnknown, method, path, err)
		}
		return fmt.Errorf("Proxmox %s %s: %w", method, path, err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		if method != http.MethodGet && response.StatusCode >= http.StatusInternalServerError {
			return fmt.Errorf("%w: Proxmox %s %s returned %s: %s", ErrOutcomeUnknown, method, path, response.Status, strings.TrimSpace(string(message)))
		}
		return &proxmoxHTTPError{StatusCode: response.StatusCode, Status: response.Status, Body: strings.TrimSpace(string(message))}
	}
	if output == nil || response.StatusCode == http.StatusNoContent {
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(output); err != nil {
		if method != http.MethodGet {
			return fmt.Errorf("%w: decode Proxmox %s %s response: %v", ErrOutcomeUnknown, method, path, err)
		}
		return fmt.Errorf("decode Proxmox %s %s response: %w", method, path, err)
	}
	return nil
}

func validSessionVMID(vmid int) bool {
	return vmid >= store.SessionVMIDMin && vmid <= store.SessionVMIDMax
}

func encodeOwnership(owner sessionOwnership) (string, error) {
	data, err := json.Marshal(owner)
	if err != nil {
		return "", fmt.Errorf("encode Session ownership metadata: %w", err)
	}
	return "codex-session:v1:" + base64.RawURLEncoding.EncodeToString(data), nil
}

func decodeOwnership(description string, owner *sessionOwnership) error {
	const prefix = "codex-session:v1:"
	if !strings.HasPrefix(description, prefix) {
		return errors.New("Proxmox guest has no recognized Session ownership metadata")
	}
	data, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(description, prefix))
	if err != nil {
		return fmt.Errorf("decode Session ownership metadata: %w", err)
	}
	if err := json.Unmarshal(data, owner); err != nil {
		return fmt.Errorf("parse Session ownership metadata: %w", err)
	}
	return nil
}
