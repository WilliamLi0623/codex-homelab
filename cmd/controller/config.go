package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/WilliamLi0623/codex-homelab/internal/capacity"
	"github.com/WilliamLi0623/codex-homelab/internal/executor/k3s"
	"github.com/WilliamLi0623/codex-homelab/internal/modelrouter"
	"github.com/WilliamLi0623/codex-homelab/internal/orchestrator"
	"github.com/WilliamLi0623/codex-homelab/internal/sessionruntime"
)

const (
	controllerVMIDMin         = 3000
	controllerVMIDMax         = 3899
	controllerTemplateVMIDMin = 3900
	controllerTemplateVMIDMax = 3902
)

type environmentConfig struct {
	Proxmox             capacity.ProxmoxConfig
	Kubernetes          k3s.KubernetesConfig
	CapacityConfig      orchestrator.CapacityAdapterConfig
	Routes              modelrouter.RouteConfig
	SessionRuntime      *sessionruntime.Config
	SessionBootstrap    *sessionruntime.BootstrapDriverConfig
	SessionProxmoxToken string
}

func loadEnvironmentConfig() (environmentConfig, error) {
	values := map[string]string{}
	var missing []string
	for _, name := range []string{
		"PROXMOX_BASE_URL", "PROXMOX_NODE", "PROXMOX_TOKEN", "PROXMOX_TEMPLATE_VMID",
		"KUBERNETES_BASE_URL", "KUBERNETES_NAMESPACE", "KUBERNETES_TOKEN",
		"KUBERNETES_WORKER_IMAGE", "KUBERNETES_SERVICE_ACCOUNT",
	} {
		value := strings.TrimSpace(os.Getenv(name))
		if value == "" {
			missing = append(missing, name)
			continue
		}
		values[name] = value
	}
	if len(missing) != 0 {
		return environmentConfig{}, fmt.Errorf("missing required controller environment variables: %s", strings.Join(missing, ", "))
	}

	templateVMID, err := strconv.Atoi(values["PROXMOX_TEMPLATE_VMID"])
	if err != nil || templateVMID < controllerTemplateVMIDMin || templateVMID > controllerTemplateVMIDMax {
		return environmentConfig{}, fmt.Errorf("PROXMOX_TEMPLATE_VMID must be an integer in %d-%d", controllerTemplateVMIDMin, controllerTemplateVMIDMax)
	}
	sessionRuntimeConfig, err := loadSessionRuntimeConfig()
	if err != nil {
		return environmentConfig{}, err
	}
	sessionBootstrapConfig, err := loadSessionBootstrapConfig(sessionRuntimeConfig != nil)
	if err != nil {
		return environmentConfig{}, err
	}
	sessionProxmoxToken := ""
	if sessionRuntimeConfig != nil {
		sessionProxmoxToken = strings.TrimSpace(os.Getenv("SESSION_PROXMOX_TOKEN"))
		if sessionProxmoxToken == "" {
			return environmentConfig{}, fmt.Errorf("SESSION_PROXMOX_TOKEN is required when Session runtime is configured")
		}
		if sessionProxmoxToken == values["PROXMOX_TOKEN"] {
			return environmentConfig{}, fmt.Errorf("SESSION_PROXMOX_TOKEN must be distinct from PROXMOX_TOKEN")
		}
	}
	model := strings.TrimSpace(os.Getenv("CODEX_MODEL"))
	modelProfile := strings.TrimSpace(os.Getenv("CODEX_MODEL_PROFILE"))
	baseURL := strings.TrimSpace(os.Getenv("CODEX_OPENAI_BASE_URL"))
	secretName := strings.TrimSpace(os.Getenv("CODEX_MODEL_SECRET_NAME"))
	wireAPI := strings.TrimSpace(os.Getenv("CODEX_WIRE_API"))
	reasoningEffort, reasoningErr := configuredReasoningEffort(model, modelProfile, os.Getenv("CODEX_MODEL_REASONING_EFFORT"))
	if reasoningErr != nil {
		return environmentConfig{}, reasoningErr
	}
	if model != "" || wireAPI != "" || baseURL != "" || secretName != "" {
		if model == "" || wireAPI == "" || baseURL == "" || secretName == "" {
			return environmentConfig{}, fmt.Errorf("CODEX_MODEL, CODEX_WIRE_API, CODEX_OPENAI_BASE_URL, and CODEX_MODEL_SECRET_NAME must be configured together")
		}
		if wireAPI != "responses" && wireAPI != "chat-completions" {
			return environmentConfig{}, fmt.Errorf("unsupported CODEX_WIRE_API %q", wireAPI)
		}
		if model == "glm-5.3-flash" && wireAPI != "chat-completions" {
			return environmentConfig{}, fmt.Errorf("glm-5.3-flash requires CODEX_WIRE_API=chat-completions")
		}
	}
	routes, routeErr := loadRouteConfig()
	if routeErr != nil {
		return environmentConfig{}, routeErr
	}
	queueName := strings.TrimSpace(os.Getenv("KUEUE_QUEUE_NAME"))
	cpuRequest := strings.TrimSpace(os.Getenv("KUBERNETES_CPU_REQUEST"))
	memoryRequest := strings.TrimSpace(os.Getenv("KUBERNETES_MEMORY_REQUEST"))
	cpuLimit := strings.TrimSpace(os.Getenv("KUBERNETES_CPU_LIMIT"))
	memoryLimit := strings.TrimSpace(os.Getenv("KUBERNETES_MEMORY_LIMIT"))
	config := environmentConfig{
		Proxmox: capacity.ProxmoxConfig{
			BaseURL: values["PROXMOX_BASE_URL"],
			Node:    values["PROXMOX_NODE"],
			Token:   values["PROXMOX_TOKEN"],
			Pool:    strings.TrimSpace(os.Getenv("PROXMOX_POOL")),
			Range:   capacity.VMIDRange{Min: controllerVMIDMin, Max: controllerVMIDMax},
		},
		Kubernetes: k3s.KubernetesConfig{
			BaseURL:         values["KUBERNETES_BASE_URL"],
			Namespace:       values["KUBERNETES_NAMESPACE"],
			Token:           values["KUBERNETES_TOKEN"],
			WorkerImage:     values["KUBERNETES_WORKER_IMAGE"],
			ServiceAccount:  values["KUBERNETES_SERVICE_ACCOUNT"],
			Model:           model,
			ModelProfile:    modelProfile,
			WireAPI:         wireAPI,
			ReasoningEffort: reasoningEffort,
			OpenAIBaseURL:   baseURL,
			ModelSecretName: secretName,
			ModelSecretKey:  strings.TrimSpace(os.Getenv("CODEX_MODEL_SECRET_KEY")),
			QueueName:       queueName,
			CPURequest:      cpuRequest,
			MemoryRequest:   memoryRequest,
			CPULimit:        cpuLimit,
			MemoryLimit:     memoryLimit,
		},
		CapacityConfig: orchestrator.CapacityAdapterConfig{
			TemplateVMID: templateVMID,
			Priority:     1,
		},
		Routes:              routes,
		SessionRuntime:      sessionRuntimeConfig,
		SessionBootstrap:    sessionBootstrapConfig,
		SessionProxmoxToken: sessionProxmoxToken,
	}
	if err := config.Proxmox.ValidateConfig(); err != nil {
		return environmentConfig{}, fmt.Errorf("invalid Proxmox configuration: %w", err)
	}
	if err := config.Kubernetes.ValidateConfig(); err != nil {
		return environmentConfig{}, fmt.Errorf("invalid Kubernetes configuration: %w", err)
	}
	return config, nil
}

func loadSessionBootstrapConfig(sessionRuntimeConfigured bool) (*sessionruntime.BootstrapDriverConfig, error) {
	enabledValue := strings.TrimSpace(os.Getenv("SESSION_BOOTSTRAP_ENABLED"))
	enabled := false
	switch strings.ToLower(enabledValue) {
	case "", "false":
	case "true":
		enabled = true
	default:
		return nil, fmt.Errorf("SESSION_BOOTSTRAP_ENABLED must be true or false")
	}
	paths := map[string]string{
		"SESSION_SSH_MATERIAL_ROOT":       strings.TrimSpace(os.Getenv("SESSION_SSH_MATERIAL_ROOT")),
		"SESSION_SSH_KEYGEN":              strings.TrimSpace(os.Getenv("SESSION_SSH_KEYGEN")),
		"SESSION_BOOTSTRAP_BACKUP_ROOT":   strings.TrimSpace(os.Getenv("SESSION_BOOTSTRAP_BACKUP_ROOT")),
		"SESSION_BOOTSTRAP_ARTIFACT_ROOT": strings.TrimSpace(os.Getenv("SESSION_BOOTSTRAP_ARTIFACT_ROOT")),
		"SESSION_BOOTSTRAP_HELPER_PATH":   strings.TrimSpace(os.Getenv("SESSION_BOOTSTRAP_HELPER_PATH")),
		"SESSION_SSH_EXECUTABLE":          strings.TrimSpace(os.Getenv("SESSION_SSH_EXECUTABLE")),
	}
	helperPin := strings.TrimSpace(os.Getenv("SESSION_BOOTSTRAP_HELPER_SHA256"))
	configuredPaths := helperPin != ""
	for _, value := range paths {
		configuredPaths = configuredPaths || value != ""
	}
	if !enabled {
		if configuredPaths {
			return nil, fmt.Errorf("Session bootstrap paths require SESSION_BOOTSTRAP_ENABLED=true")
		}
		return nil, nil
	}
	codexVersion := strings.TrimSpace(os.Getenv("SESSION_BOOTSTRAP_CODEX_VERSION"))
	if codexVersion == "" {
		codexVersion = "0.160.0"
	}
	if codexVersion != "0.155.0" && codexVersion != "0.160.0" {
		return nil, fmt.Errorf("SESSION_BOOTSTRAP_CODEX_VERSION must be a pinned supported release")
	}
	if !sessionRuntimeConfigured {
		return nil, fmt.Errorf("Session bootstrap requires Session runtime configuration")
	}
	for name, value := range paths {
		if value == "" {
			return nil, fmt.Errorf("%s is required when Session bootstrap is enabled", name)
		}
	}
	if len(helperPin) != 64 || strings.Trim(helperPin, "0123456789abcdef") != "" {
		return nil, fmt.Errorf("SESSION_BOOTSTRAP_HELPER_SHA256 must be an explicit lowercase SHA256 pin")
	}
	return &sessionruntime.BootstrapDriverConfig{
		SSHMaterialRoot:    paths["SESSION_SSH_MATERIAL_ROOT"],
		SSHKeygen:          paths["SESSION_SSH_KEYGEN"],
		BackupRoot:         paths["SESSION_BOOTSTRAP_BACKUP_ROOT"],
		ArtifactRoot:       paths["SESSION_BOOTSTRAP_ARTIFACT_ROOT"],
		HelperArtifactPath: paths["SESSION_BOOTSTRAP_HELPER_PATH"],
		HelperSHA256:       helperPin,
		CodexVersion:       codexVersion,
		SSHExecutable:      paths["SESSION_SSH_EXECUTABLE"],
	}, nil
}

func loadRouteConfig() (modelrouter.RouteConfig, error) {
	openAIBase := strings.TrimSpace(os.Getenv("CODEX_OPENAI_ROUTE_BASE_URL"))
	openAISecret := strings.TrimSpace(os.Getenv("CODEX_OPENAI_ROUTE_SECRET_NAME"))
	openAIKey := strings.TrimSpace(os.Getenv("CODEX_OPENAI_ROUTE_SECRET_KEY"))
	cchBase := strings.TrimSpace(os.Getenv("CODEX_CCH_ROUTE_BASE_URL"))
	cchSecret := strings.TrimSpace(os.Getenv("CODEX_CCH_ROUTE_SECRET_NAME"))
	cchKey := strings.TrimSpace(os.Getenv("CODEX_CCH_ROUTE_SECRET_KEY"))
	configured := openAIBase != "" || openAISecret != "" || openAIKey != "" || cchBase != "" || cchSecret != "" || cchKey != ""
	if !configured {
		return modelrouter.RouteConfig{}, nil
	}
	if openAIBase == "" {
		openAIBase = "https://api.openai.com/v1"
	}
	if cchBase == "" {
		cchBase = "https://cch-jp.zenkexi.com/v1"
	}
	if openAISecret == "" || openAIKey == "" || cchSecret == "" || cchKey == "" {
		return modelrouter.RouteConfig{}, fmt.Errorf("route configuration requires OpenAI and CC Hub Kubernetes Secret references")
	}
	return modelrouter.RouteConfig{
		OpenAI: modelrouter.RouteSettings{Provider: "openai", Model: "gpt-6-luna", WireAPI: modelrouter.WireAPIResponses, ReasoningEffort: "high", BaseURL: openAIBase, SecretName: openAISecret, SecretKey: openAIKey},
		Spark:  modelrouter.RouteSettings{Provider: "cch", Model: "muse-spark-1.3-contributor", WireAPI: modelrouter.WireAPIResponses, ReasoningEffort: "xhigh", BaseURL: cchBase, SecretName: cchSecret, SecretKey: cchKey},
		GLM:    modelrouter.RouteSettings{Provider: "cch", Model: "glm-5.3-flash", WireAPI: modelrouter.WireAPIChatCompletions, ReasoningEffort: "max", BaseURL: cchBase, SecretName: cchSecret, SecretKey: cchKey},
	}, nil
}

// routingStateTokenFromEnvironment fails closed: an absent or short token
// leaves the internal quota-state endpoint disabled.
func routingStateTokenFromEnvironment() string {
	token := strings.TrimSpace(os.Getenv("CODEX_ROUTING_STATE_TOKEN"))
	if len(token) < 32 {
		return ""
	}
	return token
}

func configuredReasoningEffort(model, profile, configured string) (string, error) {
	selected := strings.TrimSpace(model)
	if selected != "muse-spark-1.3-contributor" && selected != "glm-5.3-flash" {
		selected = strings.TrimSpace(profile)
	}
	want := map[string]string{
		"glm-5.3-flash":              "max",
		"muse-spark-1.3-contributor": "xhigh",
	}[selected]
	configured = strings.TrimSpace(configured)
	if want == "" {
		return configured, nil
	}
	if configured == "" {
		return want, nil
	}
	if configured != want {
		return "", fmt.Errorf("model %q requires CODEX_MODEL_REASONING_EFFORT=%q", selected, want)

	}
	return configured, nil
}

func loadSessionRuntimeConfig() (*sessionruntime.Config, error) {
	templateValue := strings.TrimSpace(os.Getenv("SESSION_RUNTIME_TEMPLATE_VMID"))
	workspaceSizeValue := strings.TrimSpace(os.Getenv("SESSION_WORKSPACE_SIZE_GIB"))
	if templateValue == "" && workspaceSizeValue == "" {
		return nil, nil
	}
	if templateValue == "" || workspaceSizeValue == "" {
		return nil, fmt.Errorf("SESSION_RUNTIME_TEMPLATE_VMID and SESSION_WORKSPACE_SIZE_GIB must be configured together")
	}
	templateVMID, err := strconv.Atoi(templateValue)
	if err != nil || templateVMID < controllerTemplateVMIDMin || templateVMID > controllerTemplateVMIDMax {
		return nil, fmt.Errorf("SESSION_RUNTIME_TEMPLATE_VMID must be an integer in %d-%d", controllerTemplateVMIDMin, controllerTemplateVMIDMax)
	}
	workspaceSizeGiB, err := strconv.Atoi(workspaceSizeValue)
	if err != nil || workspaceSizeGiB < 1 {
		return nil, fmt.Errorf("SESSION_WORKSPACE_SIZE_GIB must be a positive integer")
	}
	return &sessionruntime.Config{TemplateVMID: templateVMID, SystemStorage: sessionruntime.SessionSystemStorage, WorkspaceStorage: "pool", WorkspaceSizeGiB: workspaceSizeGiB}, nil
}
