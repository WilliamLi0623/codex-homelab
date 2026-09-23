package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/WilliamLi0623/codex-homelab/internal/capacity"
	"github.com/WilliamLi0623/codex-homelab/internal/executor/k3s"
	"github.com/WilliamLi0623/codex-homelab/internal/orchestrator"
)

const (
	controllerVMIDMin         = 3000
	controllerVMIDMax         = 3899
	controllerTemplateVMIDMin = 3900
	controllerTemplateVMIDMax = 3902
)

type environmentConfig struct {
	Proxmox        capacity.ProxmoxConfig
	Kubernetes     k3s.KubernetesConfig
	CapacityConfig orchestrator.CapacityAdapterConfig
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
	}
	if err := config.Proxmox.ValidateConfig(); err != nil {
		return environmentConfig{}, fmt.Errorf("invalid Proxmox configuration: %w", err)
	}
	if err := config.Kubernetes.ValidateConfig(); err != nil {
		return environmentConfig{}, fmt.Errorf("invalid Kubernetes configuration: %w", err)
	}
	return config, nil
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
