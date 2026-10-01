package sessionruntime

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/WilliamLi0623/codex-homelab/internal/store"
)

func TestBootstrapNetworkStageUsesDigestAndReconcilesReadback(t *testing.T) {
	binding := consoleTestBinding()
	binding.ID = "binding-a"
	owner, err := encodeOwnership(sessionOwnership{Version: 1, SessionID: binding.SessionID, EpochID: binding.EpochID, Generation: binding.Generation})
	if err != nil {
		t.Fatal(err)
	}
	config := map[string]any{
		"vmid": binding.VMID, "hostname": sessionHostname(binding.SessionID, binding.EpochID, binding.Generation),
		"description": owner, "unprivileged": 0, "onboot": 0, "cmode": "shell",
		"rootfs": "local:subvol-4005-disk-0,size=8G", "mp0": "pool:subvol-4005-disk-1,mp=/workspace",
		"digest": "0123456789012345678901234567890123456789",
		"net0":   "name=eth0,bridge=vmbr0,ip=dhcp,link_down=1,tag=20",
	}
	var puts int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api2/json/nodes/pve-node/lxc/4005/config" {
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		switch r.Method {
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{"data": config})
		case http.MethodPut:
			puts++
			if err := r.ParseForm(); err != nil {
				t.Errorf("ParseForm: %v", err)
			}
			if r.Form.Get("digest") != "0123456789012345678901234567890123456789" || r.Form.Get("net0") != "name=eth0,bridge=vmbr0,ip=dhcp,link_down=0,tag=20" || len(r.Form) != 2 {
				t.Errorf("unsafe network patch: %v", r.Form)
			}
			config["net0"] = r.Form.Get("net0")
			_ = json.NewEncoder(w).Encode(map[string]any{"data": nil})
		default:
			t.Errorf("unexpected method %s", r.Method)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	runtime, err := NewProxmoxRuntime(ProxmoxConfig{BaseURL: server.URL, Node: "pve-node", Token: "token", Client: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	stage, err := newBootstrapNetworkStage(runtime)
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := stage.Apply(context.Background(), binding)
	if err != nil || !validBootstrapEvidence(evidence) || puts != 1 {
		t.Fatalf("Apply() evidence=%+v puts=%d err=%v", evidence, puts, err)
	}
	observed, verified, err := stage.Observe(context.Background(), binding)
	if err != nil || !verified || observed != evidence || puts != 1 {
		t.Fatalf("Observe() evidence=%+v verified=%t puts=%d err=%v", observed, verified, puts, err)
	}
}

func TestBootstrapNetworkStageNeverRetriesAmbiguousPut(t *testing.T) {
	binding := consoleTestBinding()
	binding.ID = "binding-a"
	owner, _ := encodeOwnership(sessionOwnership{Version: 1, SessionID: binding.SessionID, EpochID: binding.EpochID, Generation: binding.Generation})
	config := map[string]any{"vmid": binding.VMID, "hostname": sessionHostname(binding.SessionID, binding.EpochID, binding.Generation), "description": owner, "unprivileged": 0, "onboot": 0, "cmode": "shell", "rootfs": "local:root", "mp0": "pool:workspace,mp=/workspace", "digest": strings.Repeat("1", 40), "net0": "name=eth0,ip=dhcp,link_down=1"}
	puts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			puts++
			http.Error(w, "ambiguous", http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": config})
	}))
	defer server.Close()
	runtime, err := NewProxmoxRuntime(ProxmoxConfig{BaseURL: server.URL, Node: "pve-node", Token: "token", Client: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	stage, err := newBootstrapNetworkStage(runtime)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stage.Apply(context.Background(), binding); err == nil || puts != 1 {
		t.Fatalf("ambiguous Apply() err=%v puts=%d; must fail after exactly one PUT", err, puts)
	}
}

func TestBootstrapEnabledNetworkProjectionRejectsUnsafeConfig(t *testing.T) {
	for name, network := range map[string]string{
		"still fenced":     "name=eth0,ip=dhcp,link_down=1",
		"static address":   "name=eth0,ip=192.0.2.10/24,link_down=0",
		"duplicate option": "name=eth0,ip=dhcp,link_down=0,link_down=0",
	} {
		t.Run(name, func(t *testing.T) {
			data, _ := json.Marshal(network)
			if _, err := bootstrapEnabledNetworkProjection(map[string]json.RawMessage{"net0": data}); err == nil {
				t.Fatal("unsafe network accepted")
			}
		})
	}
	form, err := url.ParseQuery("net0=name%3Deth0%2Cip%3Ddhcp%2Clink_down%3D0")
	if err != nil || form.Get("net0") == "" {
		t.Fatalf("test form parse: %v", err)
	}
	if _, err := bootstrapNetworkEvidence(store.SessionRuntimeBinding{}, map[string]string{"net0": form.Get("net0")}); err == nil {
		t.Fatal("unbound network evidence accepted")
	}
}

func TestBootstrapNetworkCASnapshotRevalidatesOwnershipAndStorage(t *testing.T) {
	binding := consoleTestBinding()
	binding.ID = "binding-a"
	owner, err := encodeOwnership(sessionOwnership{Version: 1, SessionID: binding.SessionID, EpochID: binding.EpochID, Generation: binding.Generation})
	if err != nil {
		t.Fatal(err)
	}
	base := map[string]any{"vmid": binding.VMID, "hostname": sessionHostname(binding.SessionID, binding.EpochID, binding.Generation), "description": owner, "unprivileged": 0, "onboot": 0, "cmode": "shell", "rootfs": "local:root", "mp0": "pool:workspace,mp=/workspace", "net0": "name=eth0,ip=dhcp,link_down=1"}
	for name, mutate := range map[string]func(map[string]any){
		"wrong generation":  func(config map[string]any) { config["description"] = "not-the-owned-binding" },
		"wrong hostname":    func(config map[string]any) { config["hostname"] = "other-host" },
		"wrong VMID":        func(config map[string]any) { config["vmid"] = 4006 },
		"unprivileged":      func(config map[string]any) { config["unprivileged"] = 1 },
		"wrong system disk": func(config map[string]any) { config["rootfs"] = "pool:root" },
		"wrong workspace":   func(config map[string]any) { config["mp0"] = "local:workspace,mp=/workspace" },
		"extra mount":       func(config map[string]any) { config["mp1"] = "pool:other,mp=/other" },
	} {
		t.Run(name, func(t *testing.T) {
			config := make(map[string]any, len(base))
			for key, value := range base {
				config[key] = value
			}
			mutate(config)
			encoded := make(map[string]json.RawMessage, len(config))
			for key, value := range config {
				data, err := json.Marshal(value)
				if err != nil {
					t.Fatal(err)
				}
				encoded[key] = data
			}
			if bootstrapNetworkSnapshotMatchesBinding(encoded, binding) {
				t.Fatal("mismatched CAS snapshot accepted")
			}
		})
	}
}
