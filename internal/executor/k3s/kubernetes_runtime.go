package k3s

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

var ErrWorkerNotReady = errors.New("Kubernetes worker Pod is not ready")
var ErrResultProtocol = errors.New("agentd result protocol is invalid")
var labelValuePattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9_.-]{0,61}[A-Za-z0-9])?$`)
var resourceQuantityPattern = regexp.MustCompile(`^(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[eE][+-]?[0-9]+|n|u|m|k|M|G|T|P|E|Ki|Mi|Gi|Ti|Pi|Ei)?$`)

type KubernetesConfig struct {
	BaseURL           string
	Namespace         string
	Token             string
	WorkerImage       string
	ServiceAccount    string
	Model             string
	ModelProfile      string
	WireAPI           string
	OpenAIBaseURL     string
	ModelSecretName   string
	ModelSecretKey    string
	QueueName         string
	CPURequest        string
	MemoryRequest     string
	CPULimit          string
	MemoryLimit       string
	HTTPClient        *http.Client
	DrainTimeout      time.Duration
	DrainPollInterval time.Duration
}

type KubernetesRuntime struct{ config KubernetesConfig }

func NewKubernetesRuntime(config KubernetesConfig) *KubernetesRuntime {
	if config.HTTPClient == nil {
		config.HTTPClient = http.DefaultClient
	}
	return &KubernetesRuntime{config: config}
}

func (c KubernetesConfig) ValidateConfig() error {
	u, err := url.Parse(c.BaseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("Kubernetes BaseURL must be an http/https URL with a host")
	}
	if c.Namespace == "" || c.WorkerImage == "" || c.ServiceAccount == "" || c.Token == "" {
		return errors.New("Kubernetes namespace, worker image, service account, and token are required")
	}
	if c.QueueName == "" {
		if c.CPURequest != "" || c.MemoryRequest != "" || c.CPULimit != "" || c.MemoryLimit != "" {
			return errors.New("Kubernetes resource requests and limits require a Kueue queue")
		}
		return nil
	}
	if !validLabelValue(c.QueueName) {
		return errors.New("Kueue queue name must be a valid Kubernetes label value")
	}
	if c.CPURequest == "" || c.MemoryRequest == "" {
		return errors.New("Kueue queue requires CPU and memory requests")
	}
	for name, value := range map[string]string{
		"CPU request": c.CPURequest, "memory request": c.MemoryRequest,
		"CPU limit": c.CPULimit, "memory limit": c.MemoryLimit,
	} {
		if value != "" && !resourceQuantityPattern.MatchString(value) {
			return fmt.Errorf("invalid Kubernetes %s quantity %q", name, value)
		}
	}
	if (c.CPULimit == "") != (c.MemoryLimit == "") {
		return errors.New("Kubernetes CPU and memory limits must be configured together")
	}
	return nil
}

var dnsInvalid = regexp.MustCompile(`[^a-z0-9-]+`)

func jobName(taskID, attemptID string) string {
	h := sha256.Sum256([]byte(taskID + "\x00" + attemptID))
	base := strings.ToLower(dnsInvalid.ReplaceAllString(taskID, "-"))
	base = strings.Trim(base, "-")
	if base == "" {
		base = "task"
	}
	suffix := "-" + hex.EncodeToString(h[:])[:16]
	base = strings.Trim(base, "-")
	if len(base)+len(suffix) > 63 {
		base = strings.TrimRight(base[:63-len(suffix)], "-")
	}
	return base + suffix
}

type kJob struct {
	Metadata struct {
		Name   string            `json:"name"`
		Labels map[string]string `json:"labels"`
	} `json:"metadata"`
	Status struct {
		Active    int `json:"active"`
		Succeeded int `json:"succeeded"`
		Failed    int `json:"failed"`
	} `json:"status"`
}
type kPodList struct {
	Items []struct {
		Metadata struct {
			Name string `json:"name"`
		} `json:"metadata"`
	} `json:"items"`
}

func (r *KubernetesRuntime) CreateJob(ctx context.Context, request JobRequest) (Job, error) {
	if err := r.config.ValidateConfig(); err != nil {
		return Job{}, err
	}
	if request.ModelProfile != "" && r.config.ModelProfile != "" && request.ModelProfile != r.config.ModelProfile {
		return Job{}, fmt.Errorf("model profile %q is not configured for this worker (configured %q)", request.ModelProfile, r.config.ModelProfile)
	}
	name := jobName(request.TaskID, request.AttemptID)
	workspacePath := "/workspace/" + request.AttemptID
	if request.WorkspacePath != "" && request.WorkspacePath != workspacePath {
		return Job{}, errors.New("workspace path must be the attempt-scoped /workspace path")
	}
	labels := map[string]string{"task_id": request.TaskID, "attempt_id": request.AttemptID, "executor": executorName}
	if r.config.QueueName != "" {
		labels["kueue.x-k8s.io/queue-name"] = r.config.QueueName
	}
	if !validLabelValue(request.TaskID) || !validLabelValue(request.AttemptID) {
		return Job{}, errors.New("task_id and attempt_id must be valid Kubernetes label values of at most 63 characters")
	}
	if request.NodeName != "" && !validLabelValue(request.NodeName) {
		return Job{}, errors.New("node name must be a valid Kubernetes node label value")
	}
	var existing kJob
	err := r.doJSON(ctx, http.MethodGet, r.path("apis/batch/v1/namespaces/"+url.PathEscape(r.config.Namespace)+"/jobs/"+url.PathEscape(name)), nil, &existing)
	if err == nil {
		if !sameLabels(existing.Metadata.Labels, labels) {
			return Job{}, fmt.Errorf("existing Kubernetes Job %q has mismatched labels", name)
		}
		return Job{ID: name, TaskID: request.TaskID, AttemptID: request.AttemptID, State: jobState(existing.Status.Active, existing.Status.Succeeded, existing.Status.Failed)}, nil
	}
	if !isNotFound(err) {
		return Job{}, err
	}
	requestJSON, _ := json.Marshal(map[string]string{"prompt": request.Prompt})
	env := []any{map[string]string{"name": "CODEX_AGENTD_REQUEST", "value": string(requestJSON)}, map[string]string{"name": "CODEX_ATTEMPT_ID", "value": request.AttemptID}, map[string]string{"name": "CODEX_HOME", "value": "/work/" + request.AttemptID}}
	if request.ModelProfile != "" {
		env = append(env, map[string]string{"name": "CODEX_MODEL_PROFILE", "value": request.ModelProfile})
	}
	if r.config.Model != "" {
		env = append(env, map[string]string{"name": "CODEX_MODEL", "value": r.config.Model})
	}
	if r.config.WireAPI != "" {
		env = append(env, map[string]string{"name": "CODEX_WIRE_API", "value": r.config.WireAPI})
	}
	if r.config.OpenAIBaseURL != "" {
		env = append(env, map[string]string{"name": "CODEX_OPENAI_BASE_URL", "value": r.config.OpenAIBaseURL})
	}
	if request.Repository != "" {
		env = append(env, map[string]string{"name": "CODEX_REPOSITORY", "value": request.Repository})
	}
	if request.BaseRef != "" {
		env = append(env, map[string]string{"name": "CODEX_BASE_REF", "value": request.BaseRef})
	}
	if request.WorkspacePath != "" {
		env = append(env, map[string]string{"name": "CODEX_WORKSPACE", "value": request.WorkspacePath})
	}
	if len(request.ValidationCommand) != 0 {
		validationJSON, err := json.Marshal(request.ValidationCommand)
		if err != nil {
			return Job{}, errors.New("validation command is invalid")
		}
		env = append(env, map[string]string{"name": "CODEX_VALIDATION_COMMAND", "value": string(validationJSON)})
	}
	container := map[string]any{
		"name":    "worker",
		"image":   r.config.WorkerImage,
		"command": []string{"/bin/sh", "-c", workerCommand(r.config)},
		"env":     env,
		"volumeMounts": []any{map[string]string{
			"name":      "attempt-workspace",
			"mountPath": "/workspace",
		}},
	}
	resources := map[string]any{
		"requests": map[string]string{"cpu": r.config.CPURequest, "memory": r.config.MemoryRequest},
	}
	if r.config.CPULimit != "" {
		resources["limits"] = map[string]string{"cpu": r.config.CPULimit, "memory": r.config.MemoryLimit}
	}
	if r.config.QueueName != "" {
		container["resources"] = resources
	}
	podSpec := map[string]any{
		"restartPolicy":      "Never",
		"serviceAccountName": r.config.ServiceAccount,
		"volumes": []any{map[string]any{
			"name":     "attempt-workspace",
			"emptyDir": map[string]any{},
		}},
		"containers": []any{container},
	}
	if request.NodeName != "" {
		podSpec["nodeName"] = request.NodeName
	}
	body := map[string]any{
		"apiVersion": "batch/v1",
		"kind":       "Job",
		"metadata":   map[string]any{"name": name, "labels": labels},
		"spec": map[string]any{
			"backoffLimit": 0,
			"template": map[string]any{
				"metadata": map[string]any{"labels": labels},
				"spec":     podSpec,
			},
		},
	}
	if r.config.ModelSecretName != "" {
		containerEnv := container["env"].([]any)
		secretKey := r.config.ModelSecretKey
		if secretKey == "" {
			secretKey = "api-key"
		}
		containerEnv = append(containerEnv, map[string]any{
			"name":      "CODEX_API_KEY",
			"valueFrom": map[string]any{"secretKeyRef": map[string]string{"name": r.config.ModelSecretName, "key": secretKey}},
		})
		container["env"] = containerEnv
	}
	var created kJob
	if err := r.doJSON(ctx, http.MethodPost, r.path("apis/batch/v1/namespaces/"+url.PathEscape(r.config.Namespace)+"/jobs"), body, &created); err != nil {
		return Job{}, err
	}
	return Job{ID: name, TaskID: request.TaskID, AttemptID: request.AttemptID, State: JobPending}, nil
}

func workerCommand(config KubernetesConfig) string {
	if config.Model == "muse-spark-1.3-contributor" || config.ModelProfile == "muse-spark-1.3-contributor" {
		return "chmod 0755 /usr/local/bin/codex-agentd && export GIT_AUTHOR_NAME=codex-agent GIT_AUTHOR_EMAIL=codex-agent@localhost GIT_COMMITTER_NAME=codex-agent GIT_COMMITTER_EMAIL=codex-agent@localhost && mkdir -p \"$CODEX_HOME\" && exec /usr/local/bin/codex-agentd --listen 0.0.0.0:8080"
	}
	return "chmod 0755 /opt/codex/vendor/x86_64-unknown-linux-musl/bin/codex /usr/local/bin/codex /usr/local/bin/codex-agentd && export GIT_AUTHOR_NAME=codex-agent GIT_AUTHOR_EMAIL=codex-agent@localhost GIT_COMMITTER_NAME=codex-agent GIT_COMMITTER_EMAIL=codex-agent@localhost && mkdir -p \"$CODEX_HOME\" && printf '%s\\n' \"$CODEX_API_KEY\" | /usr/local/bin/codex login --with-api-key >/dev/null && exec /usr/local/bin/codex-agentd --listen 0.0.0.0:8080"
}

func (r *KubernetesRuntime) Observe(ctx context.Context, id string) (Job, error) {
	if err := r.config.ValidateConfig(); err != nil {
		return Job{}, err
	}
	var job kJob
	if err := r.doJSON(ctx, http.MethodGet, r.path("apis/batch/v1/namespaces/"+url.PathEscape(r.config.Namespace)+"/jobs/"+url.PathEscape(id)), nil, &job); err != nil {
		if isNotFound(err) {
			return Job{}, ErrUnknown
		}
		return Job{}, err
	}
	return Job{ID: id, TaskID: job.Metadata.Labels["task_id"], AttemptID: job.Metadata.Labels["attempt_id"], State: jobState(job.Status.Active, job.Status.Succeeded, job.Status.Failed)}, nil
}

func (r *KubernetesRuntime) SendMessage(ctx context.Context, id, message string) error {
	if err := r.config.ValidateConfig(); err != nil {
		return err
	}
	pod, err := r.workerPod(ctx, id)
	if err != nil {
		return err
	}
	return r.doJSON(ctx, http.MethodPost, r.podProxyPath(pod, "messages"), map[string]string{"prompt": message}, nil)
}

func (r *KubernetesRuntime) Cancel(ctx context.Context, id string) error {
	err := r.doJSON(ctx, http.MethodDelete, r.path("apis/batch/v1/namespaces/"+url.PathEscape(r.config.Namespace)+"/jobs/"+url.PathEscape(id)), nil, nil)
	if isNotFound(err) {
		return nil
	}
	return err
}

func (r *KubernetesRuntime) CollectResult(ctx context.Context, id string) (Result, error) {
	if err := r.config.ValidateConfig(); err != nil {
		return Result{}, err
	}
	pod, err := r.workerPod(ctx, id)
	if err != nil {
		return Result{}, err
	}
	// A successful result requires worker stdout to contain commit_sha; this adapter never fabricates it.
	data, err := r.read(ctx, http.MethodGet, r.podProxyPath(pod, "result"))
	if err != nil {
		if isNotFound(err) {
			return Result{}, ErrWorkerNotReady
		}
		return Result{}, err
	}
	var response struct {
		ThreadID  string          `json:"thread_id"`
		Events    json.RawMessage `json:"events"`
		CommitSHA string          `json:"commit_sha"`
	}
	if json.Unmarshal(bytes.TrimSpace(data), &response) != nil || response.ThreadID == "" || len(response.Events) == 0 || string(response.Events) == "null" {
		return Result{}, fmt.Errorf("%w: thread_id and events are required", ErrResultProtocol)
	}
	if len(response.CommitSHA) != 40 {
		return Result{}, fmt.Errorf("%w: commit_sha must be 40 hexadecimal characters", ErrResultProtocol)
	}
	if _, err := hex.DecodeString(response.CommitSHA); err != nil {
		return Result{}, fmt.Errorf("%w: commit_sha must be 40 hexadecimal characters", ErrResultProtocol)
	}
	return Result{Output: string(bytes.TrimSpace(data)), CommitSHA: response.CommitSHA}, nil
}

func (r *KubernetesRuntime) workerPod(ctx context.Context, id string) (string, error) {
	var pods kPodList
	selector := url.QueryEscape("job-name=" + id)
	if err := r.doJSON(ctx, http.MethodGet, r.path("api/v1/namespaces/"+url.PathEscape(r.config.Namespace)+"/pods?labelSelector="+selector), nil, &pods); err != nil {
		return "", err
	}
	if len(pods.Items) == 0 {
		return "", ErrWorkerNotReady
	}
	return pods.Items[0].Metadata.Name, nil
}

func (r *KubernetesRuntime) podProxyPath(pod, endpoint string) string {
	return r.path("api/v1/namespaces/" + url.PathEscape(r.config.Namespace) + "/pods/" + url.PathEscape(pod) + ":8080/proxy/v1/" + endpoint)
}

func (r *KubernetesRuntime) path(p string) string {
	return strings.TrimRight(r.config.BaseURL, "/") + "/" + strings.TrimLeft(p, "/")
}
func (r *KubernetesRuntime) read(ctx context.Context, method, endpoint string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, nil)
	if err != nil {
		return nil, unknown(err)
	}
	if r.config.Token != "" {
		req.Header.Set("Authorization", "Bearer "+r.config.Token)
	}
	resp, err := r.config.HTTPClient.Do(req)
	if err != nil {
		return nil, unknown(err)
	}
	defer resp.Body.Close()
	body, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return nil, unknown(readErr)
	}
	if resp.StatusCode >= 400 {
		return nil, statusError(resp.StatusCode, body)
	}
	return body, nil
}
func (r *KubernetesRuntime) doJSON(ctx context.Context, method, endpoint string, input any, output any) error {
	var body io.Reader
	if input != nil {
		data, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return unknown(err)
	}
	if input != nil {
		contentType := "application/json"
		if method == http.MethodPatch {
			contentType = "application/strategic-merge-patch+json"
		}
		req.Header.Set("Content-Type", contentType)
	}
	if r.config.Token != "" {
		req.Header.Set("Authorization", "Bearer "+r.config.Token)
	}
	resp, err := r.config.HTTPClient.Do(req)
	if err != nil {
		return unknown(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return unknown(err)
	}
	if resp.StatusCode >= 400 {
		return statusError(resp.StatusCode, data)
	}
	if output != nil && len(data) != 0 {
		if err := json.Unmarshal(data, output); err != nil {
			return fmt.Errorf("decode Kubernetes response: %w", err)
		}
	}
	return nil
}
func unknown(err error) error { return fmt.Errorf("%w: transport failure", ErrUnknown) }
func statusError(code int, body []byte) error {
	return fmt.Errorf("Kubernetes API returned HTTP %d", code)
}
func isNotFound(err error) bool { return err != nil && strings.Contains(err.Error(), "HTTP 404") }
func sameLabels(got, want map[string]string) bool {
	for k, v := range want {
		if got[k] != v {
			return false
		}
	}
	return true
}
func validLabelValue(value string) bool {
	return len(value) <= 63 && labelValuePattern.MatchString(value)
}
func jobState(active, succeeded, failed int) JobState {
	if succeeded > 0 {
		return JobSucceeded
	}
	if failed > 0 {
		return JobFailed
	}
	if active > 0 {
		return JobRunning
	}
	return JobPending
}
