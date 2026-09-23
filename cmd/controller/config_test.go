package main

import (
	"strings"
	"testing"

	"github.com/WilliamLi0623/codex-homelab/internal/modelrouter"
)

func TestRoutingStateTokenFromEnvironmentFailsClosed(t *testing.T) {
	t.Setenv("CODEX_ROUTING_STATE_TOKEN", "")
	if got := routingStateTokenFromEnvironment(); got != "" {
		t.Fatalf("missing token = %q, want disabled endpoint", got)
	}
	t.Setenv("CODEX_ROUTING_STATE_TOKEN", "too-short")
	if got := routingStateTokenFromEnvironment(); got != "" {
		t.Fatalf("short token = %q, want disabled endpoint", got)
	}
	token := "0123456789abcdef0123456789abcdef"
	t.Setenv("CODEX_ROUTING_STATE_TOKEN", token)
	if got := routingStateTokenFromEnvironment(); got != token {
		t.Fatal("valid token was not accepted")
	}
}

func TestLoadRouteConfigIsOptionalButRequiresBothSecretReferences(t *testing.T) {
	for _, name := range []string{"CODEX_OPENAI_ROUTE_BASE_URL", "CODEX_OPENAI_ROUTE_SECRET_NAME", "CODEX_OPENAI_ROUTE_SECRET_KEY", "CODEX_CCH_ROUTE_BASE_URL", "CODEX_CCH_ROUTE_SECRET_NAME", "CODEX_CCH_ROUTE_SECRET_KEY"} {
		t.Setenv(name, "")
	}
	if config, err := loadRouteConfig(); err != nil || config != (modelrouter.RouteConfig{}) {
		t.Fatalf("empty route config = %+v, %v; want disabled", config, err)
	}
	t.Setenv("CODEX_OPENAI_ROUTE_SECRET_NAME", "openai-route")
	if _, err := loadRouteConfig(); err == nil {
		t.Fatal("partial route secrets were accepted")
	}
	t.Setenv("CODEX_OPENAI_ROUTE_SECRET_KEY", "api-key")
	t.Setenv("CODEX_CCH_ROUTE_SECRET_NAME", "cch-route")
	t.Setenv("CODEX_CCH_ROUTE_SECRET_KEY", "api-key")
	config, err := loadRouteConfig()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := modelrouter.Resolve(modelrouter.ModeNormal, modelrouter.RoleWorker, config); err != nil {
		t.Fatalf("normal route configuration invalid: %v", err)
	}
	if _, err := modelrouter.Resolve(modelrouter.ModeQuotaFallback, modelrouter.RoleWorker, config); err != nil {
		t.Fatalf("fallback route configuration invalid: %v", err)
	}
}

func TestLoadEnvironmentConfigRequiresAllValuesWithoutLeakingSecrets(t *testing.T) {
	setControllerEnvironment(t)
	t.Setenv("PROXMOX_BASE_URL", "")
	const token = "proxmox-secret-token"
	t.Setenv("PROXMOX_TOKEN", token)

	_, err := loadEnvironmentConfig()
	if err == nil {
		t.Fatal("loadEnvironmentConfig() error = nil, want missing configuration error")
	}
	if strings.Contains(err.Error(), token) {
		t.Fatalf("configuration error leaked token: %q", err)
	}
}

func TestLoadEnvironmentConfigRejectsTemplateVMIDOutsideDynamicRange(t *testing.T) {
	setControllerEnvironment(t)
	t.Setenv("PROXMOX_TEMPLATE_VMID", "2999")
	const token = "proxmox-secret-token"
	t.Setenv("PROXMOX_TOKEN", token)

	_, err := loadEnvironmentConfig()
	if err == nil {
		t.Fatal("loadEnvironmentConfig() error = nil, want invalid template VMID error")
	}
	if strings.Contains(err.Error(), token) {
		t.Fatalf("configuration error leaked token: %q", err)
	}
}

func TestLoadEnvironmentConfigRejectsPartialModelSecretConfig(t *testing.T) {
	setControllerEnvironment(t)
	t.Setenv("CODEX_MODEL", "glm-5.3-flash")
	if _, err := loadEnvironmentConfig(); err == nil {
		t.Fatal("partial model config succeeded")
	}
}

func setControllerEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("PROXMOX_BASE_URL", "https://proxmox.example")
	t.Setenv("PROXMOX_NODE", "pve-node")
	t.Setenv("PROXMOX_TOKEN", "proxmox-token")
	t.Setenv("PROXMOX_TEMPLATE_VMID", "3900")
	t.Setenv("KUBERNETES_BASE_URL", "https://kubernetes.example")
	t.Setenv("KUBERNETES_NAMESPACE", "codex")
	t.Setenv("KUBERNETES_TOKEN", "kubernetes-token")
	t.Setenv("KUBERNETES_WORKER_IMAGE", "registry.example/worker:latest")
	t.Setenv("KUBERNETES_SERVICE_ACCOUNT", "codex-worker")
}

func TestLoadEnvironmentConfigRejectsCodingAgentMetadata(t *testing.T) {
	setControllerEnvironment(t)
	t.Setenv("CODEX_MODEL", "glm-5.3-flash")
	t.Setenv("CODEX_WIRE_API", "coding-agent")
	t.Setenv("CODEX_OPENAI_BASE_URL", "https://cch.example/v1")
	t.Setenv("CODEX_MODEL_SECRET_NAME", "codex-model-gateway")
	t.Setenv("CODEX_MODEL_SECRET_KEY", "api-key")
	if _, err := loadEnvironmentConfig(); err == nil {
		t.Fatal("legacy coding-agent metadata was accepted")
	}
}

func TestLoadEnvironmentConfigRejectsGLMResponsesMetadata(t *testing.T) {
	setControllerEnvironment(t)
	t.Setenv("CODEX_MODEL", "glm-5.3-flash")
	t.Setenv("CODEX_WIRE_API", "responses")
	t.Setenv("CODEX_OPENAI_BASE_URL", "https://cch.example/v1")
	t.Setenv("CODEX_MODEL_SECRET_NAME", "codex-model-gateway")
	if _, err := loadEnvironmentConfig(); err == nil {
		t.Fatal("GLM Responses metadata was accepted")
	}
}

func TestLoadEnvironmentConfigAcceptsChatCompletionsMetadata(t *testing.T) {
	setControllerEnvironment(t)
	t.Setenv("CODEX_MODEL", "glm-5.3-flash")
	t.Setenv("CODEX_WIRE_API", "chat-completions")
	t.Setenv("CODEX_OPENAI_BASE_URL", "https://cch.example/v1")
	t.Setenv("CODEX_MODEL_SECRET_NAME", "codex-model-gateway")
	t.Setenv("CODEX_MODEL_SECRET_KEY", "api-key")
	cfg, err := loadEnvironmentConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Kubernetes.Model != "glm-5.3-flash" || cfg.Kubernetes.WireAPI != "chat-completions" {
		t.Fatalf("Kubernetes provider config = %+v", cfg.Kubernetes)
	}
	if cfg.Kubernetes.ReasoningEffort != "max" {
		t.Fatalf("reasoning effort = %q, want max", cfg.Kubernetes.ReasoningEffort)
	}
}

func TestLoadEnvironmentConfigRejectsWrongGLMReasoningEffort(t *testing.T) {
	setControllerEnvironment(t)
	t.Setenv("CODEX_MODEL", "glm-5.3-flash")
	t.Setenv("CODEX_WIRE_API", "chat-completions")
	t.Setenv("CODEX_OPENAI_BASE_URL", "https://cch.example/v1")
	t.Setenv("CODEX_MODEL_SECRET_NAME", "codex-model-gateway")
	t.Setenv("CODEX_MODEL_REASONING_EFFORT", "xhigh")
	if _, err := loadEnvironmentConfig(); err == nil || !strings.Contains(err.Error(), "REASONING_EFFORT") {
		t.Fatalf("loadEnvironmentConfig() = %v; want reasoning effort validation", err)
	}
}

func TestLoadEnvironmentConfigLoadsKueueResources(t *testing.T) {
	setControllerEnvironment(t)
	t.Setenv("KUEUE_QUEUE_NAME", "default")
	t.Setenv("KUBERNETES_CPU_REQUEST", "500m")
	t.Setenv("KUBERNETES_MEMORY_REQUEST", "512Mi")
	t.Setenv("KUBERNETES_CPU_LIMIT", "1")
	t.Setenv("KUBERNETES_MEMORY_LIMIT", "1Gi")
	cfg, err := loadEnvironmentConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Kubernetes.QueueName != "default" || cfg.Kubernetes.CPURequest != "500m" || cfg.Kubernetes.MemoryRequest != "512Mi" || cfg.Kubernetes.CPULimit != "1" || cfg.Kubernetes.MemoryLimit != "1Gi" {
		t.Fatalf("Kubernetes Kueue config = %+v", cfg.Kubernetes)
	}
}

func TestLoadEnvironmentConfigRejectsPartialKueueResources(t *testing.T) {
	setControllerEnvironment(t)
	t.Setenv("KUEUE_QUEUE_NAME", "default")
	t.Setenv("KUBERNETES_CPU_REQUEST", "500m")
	if _, err := loadEnvironmentConfig(); err == nil {
		t.Fatal("partial Kueue resource config was accepted")
	}
}
