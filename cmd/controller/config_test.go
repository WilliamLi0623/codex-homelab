package main

import (
	"strings"
	"testing"

	"github.com/WilliamLi0623/codex-homelab/internal/modelrouter"
	"github.com/WilliamLi0623/codex-homelab/internal/sessionruntime"
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
	t.Setenv("SESSION_PROXMOX_TOKEN", "")
	t.Setenv("SESSION_BOOTSTRAP_ENABLED", "")
	t.Setenv("SESSION_SSH_MATERIAL_ROOT", "")
	t.Setenv("SESSION_SSH_KEYGEN", "")
	t.Setenv("SESSION_BOOTSTRAP_BACKUP_ROOT", "")
	t.Setenv("SESSION_BOOTSTRAP_ARTIFACT_ROOT", "")
	t.Setenv("SESSION_SSH_EXECUTABLE", "")
	t.Setenv("PROXMOX_TEMPLATE_VMID", "3900")
	t.Setenv("KUBERNETES_BASE_URL", "https://kubernetes.example")
	t.Setenv("KUBERNETES_NAMESPACE", "codex")
	t.Setenv("KUBERNETES_TOKEN", "kubernetes-token")
	t.Setenv("KUBERNETES_WORKER_IMAGE", "registry.example/worker:latest")
	t.Setenv("KUBERNETES_SERVICE_ACCOUNT", "codex-worker")
}

func TestLoadSessionBootstrapConfigIsDisabledByDefault(t *testing.T) {
	setControllerEnvironment(t)
	config, err := loadSessionBootstrapConfig(false)
	if err != nil {
		t.Fatalf("loadSessionBootstrapConfig() error: %v", err)
	}
	if config != nil {
		t.Fatalf("bootstrap config = %+v, want disabled", config)
	}
}

func TestLoadSessionBootstrapConfigRejectsPathsWhenDisabled(t *testing.T) {
	setControllerEnvironment(t)
	t.Setenv("SESSION_SSH_MATERIAL_ROOT", "/var/lib/codex-session/ssh")
	if _, err := loadSessionBootstrapConfig(true); err == nil {
		t.Fatal("bootstrap paths were silently accepted while bootstrap was disabled")
	}
}

func TestLoadSessionBootstrapConfigRequiresSessionRuntimeAndEveryPath(t *testing.T) {
	for _, tc := range []struct {
		name       string
		runtimeSet bool
		missing    string
	}{
		{name: "runtime", runtimeSet: false},
		{name: "backup", runtimeSet: true, missing: "SESSION_BOOTSTRAP_BACKUP_ROOT"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setControllerEnvironment(t)
			t.Setenv("SESSION_BOOTSTRAP_ENABLED", "true")
			t.Setenv("SESSION_SSH_MATERIAL_ROOT", "/var/lib/codex-session/ssh")
			t.Setenv("SESSION_SSH_KEYGEN", "/usr/bin/ssh-keygen")
			t.Setenv("SESSION_BOOTSTRAP_BACKUP_ROOT", "/var/lib/codex-session/backups")
			t.Setenv("SESSION_BOOTSTRAP_ARTIFACT_ROOT", "/var/lib/codex-bootstrap-artifacts")
			t.Setenv("SESSION_SSH_EXECUTABLE", "/usr/bin/ssh")
			if !tc.runtimeSet {
				if _, err := loadSessionBootstrapConfig(false); err == nil {
					t.Fatal("bootstrap enabled without Session runtime was accepted")
				}
				return
			}
			t.Setenv(tc.missing, "")
			if _, err := loadSessionBootstrapConfig(true); err == nil {
				t.Fatalf("bootstrap enabled without %s was accepted", tc.missing)
			}
		})
	}
}

func TestLoadSessionBootstrapConfigLoadsExactPaths(t *testing.T) {
	setControllerEnvironment(t)
	t.Setenv("SESSION_BOOTSTRAP_ENABLED", "true")
	t.Setenv("SESSION_SSH_MATERIAL_ROOT", "/var/lib/codex-session/ssh")
	t.Setenv("SESSION_SSH_KEYGEN", "/usr/bin/ssh-keygen")
	t.Setenv("SESSION_BOOTSTRAP_BACKUP_ROOT", "/var/lib/codex-session/backups")
	t.Setenv("SESSION_BOOTSTRAP_ARTIFACT_ROOT", "/var/lib/codex-bootstrap-artifacts")
	t.Setenv("SESSION_SSH_EXECUTABLE", "/usr/bin/ssh")
	config, err := loadSessionBootstrapConfig(true)
	if err != nil {
		t.Fatalf("loadSessionBootstrapConfig() error: %v", err)
	}
	want := &sessionruntime.BootstrapDriverConfig{SSHMaterialRoot: "/var/lib/codex-session/ssh", SSHKeygen: "/usr/bin/ssh-keygen", BackupRoot: "/var/lib/codex-session/backups", ArtifactRoot: "/var/lib/codex-bootstrap-artifacts", CodexVersion: "0.160.0", SSHExecutable: "/usr/bin/ssh"}
	if config == nil || *config != *want {
		t.Fatalf("bootstrap config = %+v, want %+v", config, want)
	}
}

func TestLoadSessionBootstrapConfigPinsRollbackVersionOnlyWhenExplicit(t *testing.T) {
	setControllerEnvironment(t)
	t.Setenv("SESSION_BOOTSTRAP_ENABLED", "true")
	t.Setenv("SESSION_SSH_MATERIAL_ROOT", "/var/lib/codex-session/ssh")
	t.Setenv("SESSION_SSH_KEYGEN", "/usr/bin/ssh-keygen")
	t.Setenv("SESSION_BOOTSTRAP_BACKUP_ROOT", "/var/lib/codex-session/backups")
	t.Setenv("SESSION_BOOTSTRAP_ARTIFACT_ROOT", "/var/lib/codex-bootstrap-artifacts")
	t.Setenv("SESSION_SSH_EXECUTABLE", "/usr/bin/ssh")
	t.Setenv("SESSION_BOOTSTRAP_CODEX_VERSION", "0.155.0")
	cfg, err := loadSessionBootstrapConfig(true)
	if err != nil || cfg.CodexVersion != "0.155.0" {
		t.Fatalf("rollback bootstrap config=%+v error=%v", cfg, err)
	}
	t.Setenv("SESSION_BOOTSTRAP_CODEX_VERSION", "latest")
	if _, err := loadSessionBootstrapConfig(true); err == nil {
		t.Fatal("mutable/unresolved Codex version was accepted")
	}
}

func TestDisabledSessionBootstrapIgnoresUnusedCodexVersionOverride(t *testing.T) {
	setControllerEnvironment(t)
	t.Setenv("SESSION_BOOTSTRAP_ENABLED", "false")
	t.Setenv("SESSION_BOOTSTRAP_CODEX_VERSION", "latest")
	config, err := loadSessionBootstrapConfig(true)
	if err != nil || config != nil {
		t.Fatalf("disabled bootstrap config=%+v error=%v; unused version override must not affect existing behavior", config, err)
	}
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

func TestLoadEnvironmentConfigLeavesSessionRuntimeDisabledByDefault(t *testing.T) {
	setControllerEnvironment(t)
	t.Setenv("SESSION_RUNTIME_TEMPLATE_VMID", "")
	t.Setenv("SESSION_WORKSPACE_SIZE_GIB", "")
	t.Setenv("SESSION_PROXMOX_TOKEN", "staged-session-token")

	cfg, err := loadEnvironmentConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SessionRuntime != nil {
		t.Fatalf("Session runtime config = %+v, want disabled", cfg.SessionRuntime)
	}
	if cfg.SessionProxmoxToken != "" {
		t.Fatal("staged Session Proxmox token was retained while Session runtime was disabled")
	}
}

func TestLoadEnvironmentConfigLoadsSessionRuntimeSeparatelyFromWorkerPool(t *testing.T) {
	setControllerEnvironment(t)
	t.Setenv("SESSION_RUNTIME_TEMPLATE_VMID", "3901")
	t.Setenv("SESSION_WORKSPACE_SIZE_GIB", "48")
	t.Setenv("SESSION_PROXMOX_TOKEN", "session-proxmox-token")

	cfg, err := loadEnvironmentConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SessionRuntime == nil {
		t.Fatal("Session runtime config is nil")
	}
	if cfg.SessionRuntime.TemplateVMID != 3901 || cfg.SessionRuntime.SystemStorage != "local" || cfg.SessionRuntime.WorkspaceStorage != "pool" || cfg.SessionRuntime.WorkspaceSizeGiB != 48 {
		t.Fatalf("Session runtime config = %+v", cfg.SessionRuntime)
	}
	if cfg.CapacityConfig.TemplateVMID != 3900 {
		t.Fatalf("worker template VMID changed to %d", cfg.CapacityConfig.TemplateVMID)
	}
	if cfg.Proxmox.Token != "proxmox-token" || cfg.SessionProxmoxToken != "session-proxmox-token" {
		t.Fatal("worker and Session Proxmox credentials were not kept separate")
	}
}

func TestLoadEnvironmentConfigRequiresSessionTokenWhenSessionRuntimeEnabled(t *testing.T) {
	setControllerEnvironment(t)
	t.Setenv("SESSION_RUNTIME_TEMPLATE_VMID", "3901")
	t.Setenv("SESSION_WORKSPACE_SIZE_GIB", "48")
	const workerToken = "worker-proxmox-secret"
	t.Setenv("PROXMOX_TOKEN", workerToken)
	t.Setenv("SESSION_PROXMOX_TOKEN", "")

	_, err := loadEnvironmentConfig()
	if err == nil {
		t.Fatal("Session runtime without SESSION_PROXMOX_TOKEN was accepted")
	}
	if strings.Contains(err.Error(), workerToken) {
		t.Fatalf("configuration error leaked worker token: %q", err)
	}
}

func TestLoadEnvironmentConfigRejectsReusedWorkerTokenForSessionRuntime(t *testing.T) {
	setControllerEnvironment(t)
	t.Setenv("SESSION_RUNTIME_TEMPLATE_VMID", "3901")
	t.Setenv("SESSION_WORKSPACE_SIZE_GIB", "48")
	const sharedToken = "shared-proxmox-secret"
	t.Setenv("PROXMOX_TOKEN", sharedToken)
	t.Setenv("SESSION_PROXMOX_TOKEN", sharedToken)

	_, err := loadEnvironmentConfig()
	if err == nil {
		t.Fatal("Session runtime accepted the worker Proxmox token")
	}
	if strings.Contains(err.Error(), sharedToken) {
		t.Fatalf("configuration error leaked shared token: %q", err)
	}
}

func TestLoadEnvironmentConfigRejectsPartialSessionRuntimeConfig(t *testing.T) {
	setControllerEnvironment(t)
	t.Setenv("SESSION_RUNTIME_TEMPLATE_VMID", "3900")
	t.Setenv("SESSION_WORKSPACE_SIZE_GIB", "")
	if _, err := loadEnvironmentConfig(); err == nil {
		t.Fatal("partial Session runtime config was accepted")
	}
}
