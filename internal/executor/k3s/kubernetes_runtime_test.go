package k3s

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	first, err := r.CreateJob(context.Background(), JobRequest{TaskID: "task-1", AttemptID: "attempt-1", Prompt: "hello"})
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
	if got := container["command"].([]any); len(got) != 3 || got[0] != "/bin/sh" || got[1] != "-c" {
		t.Fatalf("command=%v", got)
	}
	if strings.Contains(strings.Join([]string{fmt.Sprint(container["args"])}, " "), "--task-id") {
		t.Fatal("legacy CLI args present")
	}
	envs := container["env"].([]any)
	if !containsEnv(envs, "CODEX_AGENTD_REQUEST", `{"prompt":"hello"}`) || !containsEnv(envs, "CODEX_ATTEMPT_ID", "attempt-1") || !containsEnv(envs, "CODEX_HOME", "/work/attempt-1") {
		t.Fatalf("env=%v", envs)
	}
	command := container["command"].([]any)[2].(string)
	if !strings.Contains(command, `mkdir -p "$CODEX_HOME"`) || strings.Index(command, "mkdir -p") > strings.Index(command, "printf") {
		t.Fatalf("command=%q", command)
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

func TestKubernetesRuntimeCollectResultRequiresCompletionAndCommit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/jobs/"):
			w.Write([]byte(`{"metadata":{"name":"job"},"status":{"succeeded":1}}`))
		case r.URL.Path == "/api/v1/namespaces/ns/pods":
			w.Write([]byte(`{"items":[{"metadata":{"name":"pod"}}]}`))
		case strings.HasSuffix(r.URL.Path, "/pods/pod/log"):
			w.Write([]byte(`{"thread_id":"thread-1","events":["done"],"commit_sha":"abc123"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	r := NewKubernetesRuntime(KubernetesConfig{BaseURL: server.URL, Namespace: "ns", Token: "token", WorkerImage: "image", ServiceAccount: "sa", HTTPClient: server.Client()})
	result, err := r.CollectResult(context.Background(), "job")
	if err != nil || result.CommitSHA != "abc123" || !strings.Contains(result.Output, "thread-1") {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestKubernetesRuntimeFollowUpUnsupportedAndCancelNotFound(t *testing.T) {
	r := NewKubernetesRuntime(KubernetesConfig{BaseURL: "http://127.0.0.1:1", Namespace: "ns"})
	if err := r.SendMessage(context.Background(), "job", "hi"); !errors.Is(err, ErrFollowUpUnsupported) {
		t.Fatalf("err=%v", err)
	}
}
