package capacity

import (
	"strings"
	"testing"
)

func TestProxmoxConfigValidateConfigRejectsEmptyBaseURL(t *testing.T) {
	err := (ProxmoxConfig{Node: "pve-node", Token: "token", Range: VMIDRange{Min: 3000, Max: 3999}}).ValidateConfig()
	if err == nil || !strings.Contains(err.Error(), "BaseURL") {
		t.Fatalf("ValidateConfig() error = %v, want BaseURL error", err)
	}
}

func TestProxmoxConfigValidateConfigRejectsEmptyNode(t *testing.T) {
	err := (ProxmoxConfig{BaseURL: "https://pve.example", Token: "token", Range: VMIDRange{Min: 3000, Max: 3999}}).ValidateConfig()
	if err == nil || !strings.Contains(err.Error(), "node") {
		t.Fatalf("ValidateConfig() error = %v, want node error", err)
	}
}

func TestProxmoxConfigValidateConfigRejectsEmptyToken(t *testing.T) {
	err := (ProxmoxConfig{BaseURL: "https://pve.example", Node: "pve-node", Range: VMIDRange{Min: 3000, Max: 3999}}).ValidateConfig()
	if err == nil || !strings.Contains(err.Error(), "token") {
		t.Fatalf("ValidateConfig() error = %v, want token error", err)
	}
}

func TestProxmoxConfigValidateConfigRejectsInvalidRange(t *testing.T) {
	tests := []VMIDRange{
		{Min: 4000, Max: 3000},
		{Min: 3001, Max: 3999},
		{Min: 3000, Max: 3998},
	}
	for _, dynamicRange := range tests {
		config := ProxmoxConfig{BaseURL: "https://pve.example", Node: "pve-node", Token: "token", Range: dynamicRange}
		if err := config.ValidateConfig(); err == nil || !strings.Contains(err.Error(), "range") {
			t.Errorf("ValidateConfig(%+v) error = %v, want range error", dynamicRange, err)
		}
	}
}

func TestProxmoxConfigValidateConfigAcceptsValidConfig(t *testing.T) {
	config := ProxmoxConfig{BaseURL: "https://pve.example/", Node: "pve-node", Token: "token", Range: VMIDRange{Min: 3000, Max: 3999}}
	if err := config.ValidateConfig(); err != nil {
		t.Fatalf("ValidateConfig() error = %v", err)
	}
}
