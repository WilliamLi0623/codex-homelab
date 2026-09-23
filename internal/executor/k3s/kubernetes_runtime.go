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
	"strconv"
	"strings"
	"time"

	"github.com/WilliamLi0623/codex-homelab/internal/modelrouter"
)

var ErrWorkerNotReady = errors.New("Kubernetes worker Pod is not ready")
var ErrResultProtocol = errors.New("agentd result protocol is invalid")
var ErrExecutionNotFound = errors.New("Kubernetes execution Job not found")
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
	ReasoningEffort   string
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

type KubernetesRuntime struct {
	config       KubernetesConfig
	requireRoute bool
}

// NewKubernetesRuntime is the compatibility constructor used by isolated
// runtime tests and legacy callers that still supply model settings directly.
// Production controller assembly must use NewProductionKubernetesRuntime.
func NewKubernetesRuntime(config KubernetesConfig) *KubernetesRuntime {
	return newKubernetesRuntime(config, false)
}

// NewProductionKubernetesRuntime constructs the fail-closed runtime used by
// the controller. Every Job must carry a validated, attempt-frozen worker
// route before this runtime will contact Kubernetes.
func NewProductionKubernetesRuntime(config KubernetesConfig) *KubernetesRuntime {
	return newKubernetesRuntime(config, true)
}

func newKubernetesRuntime(config KubernetesConfig, requireRoute bool) *KubernetesRuntime {
	if config.HTTPClient == nil {
		config.HTTPClient = http.DefaultClient
	}
	return &KubernetesRuntime{config: config, requireRoute: requireRoute}
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
		UID    string            `json:"uid"`
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
			Name            string            `json:"name"`
			UID             string            `json:"uid"`
			Labels          map[string]string `json:"labels"`
			OwnerReferences []struct {
				Kind string `json:"kind"`
				Name string `json:"name"`
				UID  string `json:"uid"`
			} `json:"ownerReferences"`
		} `json:"metadata"`
	} `json:"items"`
}

func (r *KubernetesRuntime) CreateJob(ctx context.Context, request JobRequest) (Job, error) {
	if err := r.config.ValidateConfig(); err != nil {
		return Job{}, err
	}
	if r.requireRoute && request.Route == nil {
		return Job{}, errors.New("resolved worker route is required")
	}
	var route *modelrouter.ResolvedRoute
	if request.Route != nil {
		routeCopy := *request.Route
		if err := modelrouter.ValidateResolvedRoute(routeCopy); err != nil {
			return Job{}, fmt.Errorf("invalid resolved route: %w", err)
		}
		if routeCopy.Role != modelrouter.RoleWorker {
			return Job{}, fmt.Errorf("invalid resolved route: role %q is not a worker route", routeCopy.Role)
		}
		route = &routeCopy
	}
	if route == nil && request.ModelProfile != "" && r.config.ModelProfile != "" && request.ModelProfile != r.config.ModelProfile {
		return Job{}, fmt.Errorf("model profile %q is not configured for this worker (configured %q)", request.ModelProfile, r.config.ModelProfile)
	}
	model, modelProfile, wireAPI, reasoningEffort, baseURL := r.config.Model, r.config.ModelProfile, r.config.WireAPI, r.config.ReasoningEffort, r.config.OpenAIBaseURL
	secretName, secretKey := r.config.ModelSecretName, r.config.ModelSecretKey
	if route == nil && request.ModelProfile != "" {
		modelProfile = request.ModelProfile
	}
	if route != nil {
		model, modelProfile, wireAPI, reasoningEffort, baseURL = route.Model, route.Model, string(route.WireAPI), route.ReasoningEffort, route.BaseURL
		secretName, secretKey = route.SecretName, route.SecretKey
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
	if route != nil {
		labels["codex-route-mode"] = string(route.Mode)
		labels["codex-route-generation"] = strconv.FormatInt(route.Generation, 10)
		labels["codex-route-model"] = route.Model
		labels["codex-route-fingerprint"] = routeFingerprint(*route)
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
	if modelProfile != "" {
		env = append(env, map[string]string{"name": "CODEX_MODEL_PROFILE", "value": modelProfile})
	}
	if model != "" {
		env = append(env, map[string]string{"name": "CODEX_MODEL", "value": model})
	}
	if wireAPI != "" {
		env = append(env, map[string]string{"name": "CODEX_WIRE_API", "value": wireAPI})
	}
	if reasoningEffort != "" {
		env = append(env, map[string]string{"name": "CODEX_MODEL_REASONING_EFFORT", "value": reasoningEffort})
	}
	if baseURL != "" {
		env = append(env, map[string]string{"name": "CODEX_OPENAI_BASE_URL", "value": baseURL})
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
		"command": []string{"/bin/sh", "-c", workerCommandForRoute(r.config, route)},
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
	if secretName != "" {
		containerEnv := container["env"].([]any)
		if secretKey == "" {
			secretKey = "api-key"
		}
		containerEnv = append(containerEnv, map[string]any{
			"name":      "CODEX_API_KEY",
			"valueFrom": map[string]any{"secretKeyRef": map[string]string{"name": secretName, "key": secretKey}},
		})
		container["env"] = containerEnv
	}
	var created kJob
	if err := r.doJSON(ctx, http.MethodPost, r.path("apis/batch/v1/namespaces/"+url.PathEscape(r.config.Namespace)+"/jobs"), body, &created); err != nil {
		return Job{}, err
	}
	return Job{ID: name, TaskID: request.TaskID, AttemptID: request.AttemptID, State: JobPending}, nil
}

func routeFingerprint(route modelrouter.ResolvedRoute) string {
	encoded, _ := json.Marshal(route)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func workerCommand(config KubernetesConfig) string {
	if config.Model == "muse-spark-1.3-contributor" || config.ModelProfile == "muse-spark-1.3-contributor" {
		return "chmod 0755 /usr/local/bin/codex-agentd && export GIT_AUTHOR_NAME=codex-agent GIT_AUTHOR_EMAIL=codex-agent@localhost GIT_COMMITTER_NAME=codex-agent GIT_COMMITTER_EMAIL=codex-agent@localhost && mkdir -p \"$CODEX_HOME\" && exec /usr/local/bin/codex-agentd --listen 0.0.0.0:8080"
	}
	return "chmod 0755 /opt/codex/vendor/x86_64-unknown-linux-musl/bin/codex /usr/local/bin/codex /usr/local/bin/codex-agentd && export GIT_AUTHOR_NAME=codex-agent GIT_AUTHOR_EMAIL=codex-agent@localhost GIT_COMMITTER_NAME=codex-agent GIT_COMMITTER_EMAIL=codex-agent@localhost && mkdir -p \"$CODEX_HOME\" && printf '%s\\n' \"$CODEX_API_KEY\" | /usr/local/bin/codex login --with-api-key >/dev/null && exec /usr/local/bin/codex-agentd --listen 0.0.0.0:8080"
}

func workerCommandForRoute(config KubernetesConfig, route *modelrouter.ResolvedRoute) string {
	if route == nil {
		return workerCommand(config)
	}
	if route.Provider == "cch" || route.WireAPI == modelrouter.WireAPIChatCompletions {
		return "chmod 0755 /usr/local/bin/codex-agentd && export GIT_AUTHOR_NAME=codex-agent GIT_AUTHOR_EMAIL=codex-agent@localhost GIT_COMMITTER_NAME=codex-agent GIT_COMMITTER_EMAIL=codex-agent@localhost && mkdir -p \"$CODEX_HOME\" && exec /usr/local/bin/codex-agentd --listen 0.0.0.0:8080"
	}
	return "chmod 0755 /opt/codex/vendor/x86_64-unknown-linux-musl/bin/codex /usr/local/bin/codex /usr/local/bin/codex-agentd && export GIT_AUTHOR_NAME=codex-agent GIT_AUTHOR_EMAIL=codex-agent@localhost GIT_COMMITTER_NAME=codex-agent GIT_COMMITTER_EMAIL=codex-agent@localhost && mkdir -p \"$CODEX_HOME\" && printf '%s\\n' \"$CODEX_API_KEY\" | /usr/local/bin/codex login --with-api-key >/dev/null && exec /usr/local/bin/codex-agentd --listen 0.0.0.0:8080"
}

func (r *KubernetesRuntime) Observe(ctx context.Context, id, taskID, attemptID string) (Job, error) {
	if err := r.config.ValidateConfig(); err != nil {
		return Job{}, err
	}
	job, err := r.executionJob(ctx, id, taskID, attemptID)
	if errors.Is(err, ErrExecutionNotFound) {
		return Job{}, ErrUnknown
	}
	if err != nil {
		return Job{}, err
	}
	return Job{ID: id, TaskID: taskID, AttemptID: attemptID, State: jobState(job.Status.Active, job.Status.Succeeded, job.Status.Failed)}, nil
}

func (r *KubernetesRuntime) SendMessage(ctx context.Context, id, taskID, attemptID, message string) error {
	if err := r.config.ValidateConfig(); err != nil {
		return err
	}
	pod, err := r.workerPod(ctx, id, taskID, attemptID)
	if err != nil {
		return err
	}
	return r.doJSON(ctx, http.MethodPost, r.podProxyPath(pod, "messages"), map[string]string{"prompt": message}, nil)
}

func (r *KubernetesRuntime) Cancel(ctx context.Context, id, taskID, attemptID string) error {
	if err := r.config.ValidateConfig(); err != nil {
		return err
	}
	job, err := r.executionJob(ctx, id, taskID, attemptID)
	if err != nil {
		if errors.Is(err, ErrExecutionNotFound) {
			return nil
		}
		return err
	}
	err = r.deleteWithUID(ctx, r.path("apis/batch/v1/namespaces/"+url.PathEscape(r.config.Namespace)+"/jobs/"+url.PathEscape(id)), job.Metadata.UID, "Foreground")
	if isNotFound(err) {
		return nil
	}
	return err
}

func (r *KubernetesRuntime) deleteWithUID(ctx context.Context, resourcePath, uid, propagationPolicy string) error {
	if uid == "" {
		return errors.New("Kubernetes delete requires a UID precondition")
	}
	options := map[string]any{
		"apiVersion": "v1",
		"kind":       "DeleteOptions",
		"preconditions": map[string]string{
			"uid": uid,
		},
	}
	if propagationPolicy != "" {
		options["propagationPolicy"] = propagationPolicy
	}
	return r.doJSON(ctx, http.MethodDelete, resourcePath, options, nil)
}

func (r *KubernetesRuntime) CollectResult(ctx context.Context, id, taskID, attemptID string) (Result, error) {
	if err := r.config.ValidateConfig(); err != nil {
		return Result{}, err
	}
	pod, err := r.workerPod(ctx, id, taskID, attemptID)
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

func (r *KubernetesRuntime) executionJob(ctx context.Context, id, taskID, attemptID string) (kJob, error) {
	if taskID == "" || attemptID == "" {
		return kJob{}, errors.New("Kubernetes execution identity mismatch")
	}
	var job kJob
	err := r.doJSON(ctx, http.MethodGet, r.path("apis/batch/v1/namespaces/"+url.PathEscape(r.config.Namespace)+"/jobs/"+url.PathEscape(id)), nil, &job)
	if isNotFound(err) {
		return kJob{}, ErrExecutionNotFound
	}
	if err != nil {
		return kJob{}, err
	}
	if job.Metadata.Name != id || job.Metadata.UID == "" || !sameLabels(job.Metadata.Labels, map[string]string{"task_id": taskID, "attempt_id": attemptID, "executor": executorName}) {
		return kJob{}, errors.New("Kubernetes execution identity mismatch")
	}
	return job, nil
}

func (r *KubernetesRuntime) workerPod(ctx context.Context, id, taskID, attemptID string) (string, error) {
	job, err := r.executionJob(ctx, id, taskID, attemptID)
	if errors.Is(err, ErrExecutionNotFound) {
		return "", ErrUnknown
	}
	if err != nil {
		return "", err
	}
	var pods kPodList
	selector := url.QueryEscape("job-name=" + id + ",task_id=" + taskID + ",attempt_id=" + attemptID)
	if err := r.doJSON(ctx, http.MethodGet, r.path("api/v1/namespaces/"+url.PathEscape(r.config.Namespace)+"/pods?labelSelector="+selector), nil, &pods); err != nil {
		return "", err
	}
	if len(pods.Items) == 0 {
		return "", ErrWorkerNotReady
	}
	for _, pod := range pods.Items {
		if !sameLabels(pod.Metadata.Labels, map[string]string{"task_id": taskID, "attempt_id": attemptID, "executor": executorName}) {
			return "", errors.New("Kubernetes worker Pod identity mismatch")
		}
		ownedByJob := false
		for _, owner := range pod.Metadata.OwnerReferences {
			if owner.Kind == "Job" && owner.Name == id && owner.UID == job.Metadata.UID {
				ownedByJob = true
				break
			}
		}
		if !ownedByJob {
			return "", errors.New("Kubernetes worker Pod owner mismatch")
		}
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
