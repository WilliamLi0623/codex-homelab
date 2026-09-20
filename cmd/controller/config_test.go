package main

import (
	"strings"
	"testing"
)

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

func setControllerEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("PROXMOX_BASE_URL", "https://proxmox.example")
	t.Setenv("PROXMOX_NODE", "pve-node")
	t.Setenv("PROXMOX_TOKEN", "proxmox-token")
	t.Setenv("PROXMOX_TEMPLATE_VMID", "3005")
	t.Setenv("KUBERNETES_BASE_URL", "https://kubernetes.example")
	t.Setenv("KUBERNETES_NAMESPACE", "codex")
	t.Setenv("KUBERNETES_TOKEN", "kubernetes-token")
	t.Setenv("KUBERNETES_WORKER_IMAGE", "registry.example/worker:latest")
	t.Setenv("KUBERNETES_SERVICE_ACCOUNT", "codex-worker")
}
