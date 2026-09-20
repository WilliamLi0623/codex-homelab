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
	controllerVMIDMin = 3000
	controllerVMIDMax = 3999
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
	if err != nil || templateVMID < controllerVMIDMin || templateVMID > controllerVMIDMax {
		return environmentConfig{}, fmt.Errorf("PROXMOX_TEMPLATE_VMID must be an integer in %d-%d", controllerVMIDMin, controllerVMIDMax)
	}

	config := environmentConfig{
		Proxmox: capacity.ProxmoxConfig{
			BaseURL: values["PROXMOX_BASE_URL"],
			Node:    values["PROXMOX_NODE"],
			Token:   values["PROXMOX_TOKEN"],
			Range:   capacity.VMIDRange{Min: controllerVMIDMin, Max: controllerVMIDMax},
		},
		Kubernetes: k3s.KubernetesConfig{
			BaseURL:        values["KUBERNETES_BASE_URL"],
			Namespace:      values["KUBERNETES_NAMESPACE"],
			Token:          values["KUBERNETES_TOKEN"],
			WorkerImage:    values["KUBERNETES_WORKER_IMAGE"],
			ServiceAccount: values["KUBERNETES_SERVICE_ACCOUNT"],
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
