package capacity

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestProxmoxRuntimeCloneUsesOnlyDynamicVMIDs(t *testing.T) {
	var gotPath string
	var gotAuth string
	var gotForm url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		_ = r.ParseForm()
		gotForm = r.PostForm
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":"UPID:test"}`)
	}))
	defer server.Close()

	runtime := NewProxmoxRuntime(ProxmoxConfig{
		BaseURL: server.URL,
		Node:    "pve-node",
		Token:   "PVEAPIToken=runtime=redacted",
		Range:   VMIDRange{Min: 3000, Max: 3999},
		Client:  server.Client(),
	})

	node, err := runtime.Create(context.Background(), CreateRequest{VMID: 3010, TemplateVMID: 3005, Generation: "gen-1", Hostname: "codex-3010"})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if node.State != NodeCreating || node.VMID != 3010 {
		t.Fatalf("created node = %+v", node)
	}
	if gotPath != "/api2/json/nodes/pve-node/lxc/3005/clone" {
		t.Fatalf("clone path = %q", gotPath)
	}
	if gotAuth == "" {
		t.Fatal("clone request omitted injected authorization")
	}
	if gotForm.Get("newid") != "3010" || gotForm.Get("hostname") != "codex-3010" || gotForm.Get("full") != "1" {
		t.Fatalf("clone form = %v", gotForm)
	}
}

func TestProxmoxRuntimeRejectsProtectedActionBeforeHTTP(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer server.Close()

	runtime := NewProxmoxRuntime(ProxmoxConfig{BaseURL: server.URL, Node: "pve-node", Range: VMIDRange{Min: 3000, Max: 3999}, Client: server.Client()})
	if err := runtime.Start(context.Background(), 220); err == nil || !strings.Contains(err.Error(), ErrVMIDOutsideRange.Error()) {
		t.Fatalf("Start(protected VMID) error = %v", err)
	}
	if called {
		t.Fatal("protected action reached Proxmox HTTP server")
	}
}

func TestProxmoxRuntimeObservesStatusWithoutExposingToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api2/json/nodes/pve-node/lxc/3010/status/current" {
			t.Fatalf("status path = %q", r.URL.Path)
		}
		if strings.Contains(r.URL.String(), "secret") {
			t.Fatal("secret appeared in status URL")
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"vmid": 3010, "name": "codex-3010", "status": "running"}})
	}))
	defer server.Close()

	runtime := NewProxmoxRuntime(ProxmoxConfig{BaseURL: server.URL, Node: "pve-node", Token: "secret", Range: VMIDRange{Min: 3000, Max: 3999}, Client: server.Client()})
	node, err := runtime.Observe(context.Background(), 3010)
	if err != nil {
		t.Fatalf("Observe() error = %v", err)
	}
	if node.VMID != 3010 || node.State != NodeRunning || node.KubeNode != "codex-3010" {
		t.Fatalf("observed node = %+v", node)
	}
}
