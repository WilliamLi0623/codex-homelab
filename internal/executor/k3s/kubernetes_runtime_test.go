package k3s

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestKubernetesRuntimeCreateJobIsDeterministicAndIdempotent(t *testing.T) {
	var calls int
	var created bool
	var manifest map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			if !created {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"metadata":{"name":"job","labels":{"task_id":"task-1","attempt_id":"attempt-1","executor":"k3s"}}}`))
			return
		}
		calls++
		created = true
		if err := json.NewDecoder(r.Body).Decode(&manifest); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"metadata":{"name":"ignored"}}`))
	}))
	defer server.Close()
	r := NewKubernetesRuntime(KubernetesConfig{BaseURL: server.URL, Namespace: "default", Token: "secret", WorkerImage: "worker:latest", ServiceAccount: "sa", HTTPClient: server.Client()})
	first, err := r.CreateJob(context.Background(), JobRequest{TaskID: "task-1", AttemptID: "attempt-1", Prompt: "hello", Repository: "owner/repo", BaseRef: "main", WorkspacePath: "/workspace/attempt-1", ValidationCommand: []string{"go", "test", "./..."}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := r.CreateJob(context.Background(), JobRequest{TaskID: "task-1", AttemptID: "attempt-1", Prompt: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID || calls != 1 || strings.Contains(first.ID, "/") {
		t.Fatalf("job=%+v second=%+v calls=%d", first, second, calls)
	}
	spec := manifest["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
	container := spec["containers"].([]any)[0].(map[string]any)
	volumes := spec["volumes"].([]any)
	if len(volumes) != 1 || volumes[0].(map[string]any)["name"] != "attempt-workspace" {
		t.Fatalf("volumes=%v", volumes)
	}
	mounts := container["volumeMounts"].([]any)
	if len(mounts) != 1 || mounts[0].(map[string]any)["name"] != "attempt-workspace" || mounts[0].(map[string]any)["mountPath"] != "/workspace" {
		t.Fatalf("volumeMounts=%v", mounts)
	}
	if got := container["command"].([]any); len(got) != 3 || got[0] != "/bin/sh" || got[1] != "-c" {
		t.Fatalf("command=%v", got)
	}
	if strings.Contains(strings.Join([]string{fmt.Sprint(container["args"])}, " "), "--task-id") {
		t.Fatal("legacy CLI args present")
	}
	envs := container["env"].([]any)
	if !containsEnv(envs, "CODEX_AGENTD_REQUEST", `{"prompt":"hello"}`) || !containsEnv(envs, "CODEX_ATTEMPT_ID", "attempt-1") || !containsEnv(envs, "CODEX_HOME", "/work/attempt-1") || !containsEnv(envs, "CODEX_REPOSITORY", "owner/repo") || !containsEnv(envs, "CODEX_BASE_REF", "main") || !containsEnv(envs, "CODEX_WORKSPACE", "/workspace/attempt-1") || !containsEnv(envs, "CODEX_VALIDATION_COMMAND", `["go","test","./..."]`) {
		t.Fatalf("env=%v", envs)
	}
	command := container["command"].([]any)[2].(string)
	if !strings.Contains(command, `mkdir -p "$CODEX_HOME"`) || !strings.Contains(command, "/usr/local/bin/codex-agentd --listen 0.0.0.0:8080") || !strings.Contains(command, "exec") {
		t.Fatalf("command=%q", command)
	}
}

func TestKubernetesRuntimeRejectsNonAttemptWorkspacePath(t *testing.T) {
	r := NewKubernetesRuntime(KubernetesConfig{BaseURL: "http://127.0.0.1", Namespace: "default", Token: "secret", WorkerImage: "worker:latest", ServiceAccount: "sa"})
	if _, err := r.CreateJob(context.Background(), JobRequest{TaskID: "task-1", AttemptID: "attempt-1", WorkspacePath: "/workspace/other"}); err == nil {
		t.Fatal("CreateJob accepted a workspace outside the attempt scope")
	}
}

func TestKubernetesRuntimeNodeLifecycleCordonDrainRemoveAndVerify(t *testing.T) {
	activePodDeleted := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPatch && r.URL.Path == "/api/v1/nodes/codex-node":
			var patch map[string]map[string]bool
			if err := json.NewDecoder(r.Body).Decode(&patch); err != nil || !patch["spec"]["unschedulable"] {
				t.Fatalf("cordon patch = %+v, err = %v", patch, err)
			}
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/pods":
			if activePodDeleted {
				_, _ = io.WriteString(w, `{"items":[{"metadata":{"name":"daemon","namespace":"kube-system","ownerReferences":[{"kind":"DaemonSet"}]},"status":{"phase":"Running"}}]}`)
			} else {
				_, _ = io.WriteString(w, `{"items":[{"metadata":{"name":"work","namespace":"default"},"status":{"phase":"Running"}},{"metadata":{"name":"daemon","namespace":"kube-system","ownerReferences":[{"kind":"DaemonSet"}]},"status":{"phase":"Running"}},{"metadata":{"name":"mirror","namespace":"kube-system","annotations":{"kubernetes.io/config.mirror":"hash"}},"status":{"phase":"Running"}}]}`)
			}
		case r.Method == http.MethodDelete && r.URL.Path == "/api/v1/namespaces/default/pods/work":
			activePodDeleted = true
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodDelete && r.URL.Path == "/api/v1/nodes/codex-node":
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/nodes/codex-node":
			w.WriteHeader(http.StatusNotFound)
		default:
			t.Fatalf("unexpected node lifecycle request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()
	r := NewKubernetesRuntime(KubernetesConfig{BaseURL: server.URL, Namespace: "default", Token: "secret", WorkerImage: "worker:latest", ServiceAccount: "sa", HTTPClient: server.Client(), DrainTimeout: time.Second, DrainPollInterval: time.Millisecond})
	if err := r.Cordon(context.Background(), "codex-node"); err != nil {
		t.Fatalf("Cordon() = %v", err)
	}
	if err := r.Drain(context.Background(), "codex-node"); err != nil {
		t.Fatalf("Drain() = %v", err)
	}
	if err := r.RemoveNode(context.Background(), "codex-node"); err != nil {
		t.Fatalf("RemoveNode() = %v", err)
	}
	if err := r.VerifyNodeRemoved(context.Background(), "codex-node"); err != nil {
		t.Fatalf("VerifyNodeRemoved() = %v", err)
	}
}

func containsEnv(envs []any, name, value string) bool {
	for _, raw := range envs {
		e := raw.(map[string]any)
		if e["name"] == name && e["value"] == value {
			return true
		}
	}
	return false
}

func TestKubernetesRuntimeConfigValidationAndFailClosed(t *testing.T) {
	for _, config := range []KubernetesConfig{{}, {BaseURL: "ftp://host", Namespace: "ns", WorkerImage: "image", ServiceAccount: "sa", Token: "x"}, {BaseURL: "http://host", Namespace: "", WorkerImage: "image", ServiceAccount: "sa", Token: "x"}, {BaseURL: "http://host", Namespace: "ns", WorkerImage: "", ServiceAccount: "sa", Token: "x"}, {BaseURL: "http://host", Namespace: "ns", WorkerImage: "image", ServiceAccount: "sa", Token: ""}} {
		if err := config.ValidateConfig(); err == nil {
			t.Fatalf("config unexpectedly valid: %+v", config)
		}
	}
	r := NewKubernetesRuntime(KubernetesConfig{})
	if _, err := r.CreateJob(context.Background(), JobRequest{TaskID: "t", AttemptID: "a"}); err == nil {
		t.Fatal("CreateJob accepted invalid config")
	}
	if _, err := r.Observe(context.Background(), "job"); err == nil {
		t.Fatal("Observe accepted invalid config")
	}
	if err := r.SendMessage(context.Background(), "job", "message"); err == nil {
		t.Fatal("SendMessage accepted invalid config")
	}
	if _, err := r.CollectResult(context.Background(), "job"); err == nil {
		t.Fatal("CollectResult accepted invalid config")
	}
}

func TestKubernetesRuntimeLongAndEmptyIdentifiersHaveDNS1123Names(t *testing.T) {
	for _, ids := range [][2]string{{strings.Repeat("T", 200), "attempt"}, {"", ""}} {
		name := jobName(ids[0], ids[1])
		if len(name) > 63 || name[0] == '-' || name[len(name)-1] == '-' || strings.ToLower(name) != name {
			t.Fatalf("invalid name %q", name)
		}
	}
}

func TestKubernetesRuntimeObserveNotFoundIsUnknown(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNotFound) }))
	defer server.Close()
	r := NewKubernetesRuntime(KubernetesConfig{BaseURL: server.URL, Namespace: "ns", WorkerImage: "image", ServiceAccount: "sa", Token: "token", HTTPClient: server.Client()})
	_, err := r.Observe(context.Background(), "job")
	if !errors.Is(err, ErrUnknown) {
		t.Fatalf("err=%v", err)
	}
}

func TestKubernetesRuntimeFourXXDoesNotEchoResponseBody(t *testing.T) {
	secret := "echo-secret-token"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(secret))
	}))
	defer server.Close()
	r := NewKubernetesRuntime(KubernetesConfig{BaseURL: server.URL, Namespace: "ns", WorkerImage: "image", ServiceAccount: "sa", Token: "token", HTTPClient: server.Client()})
	_, err := r.Observe(context.Background(), "job")
	if err == nil || !strings.Contains(err.Error(), "HTTP 403") || strings.Contains(err.Error(), secret) {
		t.Fatalf("err=%v", err)
	}
}

func TestKubernetesRuntimeRejectsInvalidLabelValues(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Fatal("request should be rejected before HTTP") }))
	defer server.Close()
	r := NewKubernetesRuntime(KubernetesConfig{BaseURL: server.URL, Namespace: "ns", WorkerImage: "image", ServiceAccount: "sa", Token: "token", HTTPClient: server.Client()})
	for _, req := range []JobRequest{{TaskID: "bad/value", AttemptID: "a"}, {TaskID: strings.Repeat("a", 64), AttemptID: "a"}, {TaskID: "task", AttemptID: strings.Repeat("a", 64)}} {
		if _, err := r.CreateJob(context.Background(), req); err == nil {
			t.Fatalf("request accepted: %+v", req)
		}
	}
}

func TestKubernetesRuntimeObserveMapsStates(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"metadata":{"name":"job"},"status":{"active":1}}`))
	}))
	defer server.Close()
	r := NewKubernetesRuntime(KubernetesConfig{BaseURL: server.URL, Namespace: "ns", Token: "token", WorkerImage: "image", ServiceAccount: "sa", HTTPClient: server.Client()})
	job, err := r.Observe(context.Background(), "job")
	if err != nil || job.State != JobRunning {
		t.Fatalf("job=%+v err=%v", job, err)
	}
}

func TestKubernetesRuntimeTimeoutIsUnknownAndTokenIsNotLeaked(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
	}))
	defer server.Close()
	token := "super-secret-token"
	r := NewKubernetesRuntime(KubernetesConfig{BaseURL: server.URL, Namespace: "ns", Token: token, WorkerImage: "image", ServiceAccount: "sa", HTTPClient: &http.Client{Timeout: time.Millisecond}})
	_, err := r.Observe(context.Background(), "job")
	if !errors.Is(err, ErrUnknown) || strings.Contains(err.Error(), token) {
		t.Fatalf("err=%v", err)
	}
}

func TestKubernetesRuntimeCollectResultUsesPersistentResultProxy(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v1/namespaces/ns/pods":
			if r.Method != http.MethodGet || r.URL.Query().Get("labelSelector") != "job-name=job" {
				t.Errorf("pod list request: method=%s query=%s", r.Method, r.URL.RawQuery)
			}
			w.Write([]byte(`{"items":[{"metadata":{"name":"pod"}}]}`))
		case r.URL.Path == "/api/v1/namespaces/ns/pods/pod:8080/proxy/v1/result":
			if r.Method != http.MethodGet {
				t.Errorf("result proxy method=%s", r.Method)
			}
			w.Write([]byte(`{"thread_id":"thread-1","events":["done"],"commit_sha":"0123456789abcdef0123456789abcdef01234567"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	r := NewKubernetesRuntime(KubernetesConfig{BaseURL: server.URL, Namespace: "ns", Token: "token", WorkerImage: "image", ServiceAccount: "sa", HTTPClient: server.Client()})
	result, err := r.CollectResult(context.Background(), "job")
	if err != nil || result.CommitSHA != "0123456789abcdef0123456789abcdef01234567" || !strings.Contains(result.Output, "thread-1") {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestKubernetesRuntimeFollowUpUnsupportedAndCancelNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"items":[]}`))
	}))
	defer server.Close()
	r := NewKubernetesRuntime(KubernetesConfig{BaseURL: server.URL, Namespace: "ns", Token: "token", WorkerImage: "image", ServiceAccount: "sa", HTTPClient: server.Client()})
	if err := r.SendMessage(context.Background(), "job", "hi"); !errors.Is(err, ErrWorkerNotReady) {
		t.Fatalf("err=%v", err)
	}
}

func TestKubernetesRuntimeSendMessageUsesPodProxy(t *testing.T) {
	var gotPath, gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/namespaces/ns/pods" {
			if r.Method != http.MethodGet || r.URL.Query().Get("labelSelector") != "job-name=job" {
				t.Errorf("pod list request: method=%s query=%s", r.Method, r.URL.RawQuery)
			}
			w.Write([]byte(`{"items":[{"metadata":{"name":"pod"}}]}`))
			return
		}
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/namespaces/ns/pods/pod:8080/proxy/v1/messages" {
			t.Errorf("proxy request: method=%s path=%s", r.Method, r.URL.Path)
		}
		gotPath = r.URL.Path
		data, _ := io.ReadAll(r.Body)
		gotBody = string(data)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	r := NewKubernetesRuntime(KubernetesConfig{BaseURL: server.URL, Namespace: "ns", Token: "token", WorkerImage: "image", ServiceAccount: "sa", HTTPClient: server.Client()})
	if err := r.SendMessage(context.Background(), "job", "follow up"); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/v1/namespaces/ns/pods/pod:8080/proxy/v1/messages" || gotBody != `{"prompt":"follow up"}` {
		t.Fatalf("path=%q body=%q", gotPath, gotBody)
	}
}

func TestKubernetesRuntimeCollectResultRequiresCommitSHA(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/namespaces/ns/pods" {
			if r.Method != http.MethodGet || r.URL.Query().Get("labelSelector") != "job-name=job" {
				t.Errorf("pod list request: method=%s query=%s", r.Method, r.URL.RawQuery)
			}
			w.Write([]byte(`{"items":[{"metadata":{"name":"pod"}}]}`))
			return
		}
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/namespaces/ns/pods/pod:8080/proxy/v1/result" {
			t.Errorf("result proxy request: method=%s path=%s", r.Method, r.URL.Path)
		}
		w.Write([]byte(`{"thread_id":"thread-1","events":["done"]}`))
	}))
	defer server.Close()
	r := NewKubernetesRuntime(KubernetesConfig{BaseURL: server.URL, Namespace: "ns", Token: "token", WorkerImage: "image", ServiceAccount: "sa", HTTPClient: server.Client()})
	if _, err := r.CollectResult(context.Background(), "job"); !errors.Is(err, ErrResultProtocol) {
		t.Fatalf("err=%v", err)
	}
}

func TestKubernetesRuntimeCollectResultRejectsInvalidCommitSHA(t *testing.T) {
	for _, commitSHA := range []string{"abc123", "0123456789abcdef0123456789abcdef0123456z"} {
		t.Run(commitSHA, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v1/namespaces/ns/pods" {
					w.Write([]byte(`{"items":[{"metadata":{"name":"pod"}}]}`))
					return
				}
				w.Write([]byte(`{"thread_id":"thread-1","events":["done"],"commit_sha":"` + commitSHA + `"}`))
			}))
			defer server.Close()
			r := NewKubernetesRuntime(KubernetesConfig{BaseURL: server.URL, Namespace: "ns", Token: "token", WorkerImage: "image", ServiceAccount: "sa", HTTPClient: server.Client()})
			if _, err := r.CollectResult(context.Background(), "job"); !errors.Is(err, ErrResultProtocol) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}
