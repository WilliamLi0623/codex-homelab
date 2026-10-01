package sessionruntime

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBootstrapIsolationRequiresOwnedFencedConfigSnapshot(t *testing.T) {
	binding := consoleTestBinding()
	owner, err := encodeOwnership(sessionOwnership{Version: 1, SessionID: binding.SessionID, EpochID: binding.EpochID, Generation: binding.Generation})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		change func(map[string]any)
		valid  bool
	}{
		{"fenced", func(map[string]any) {}, true},
		{"enabled network", func(c map[string]any) { c["net0"] = "name=eth0,bridge=vmbr0,ip=dhcp,link_down=0" }, false},
		{"second enabled NIC", func(c map[string]any) { c["net1"] = "name=eth1,bridge=vmbr0,ip=dhcp" }, false},
		{"missing NIC", func(c map[string]any) { delete(c, "net0") }, false},
		{"duplicate fence option", func(c map[string]any) { c["net0"] = "name=eth0,ip=dhcp,link_down=1,link_down=0" }, false},
		{"onboot", func(c map[string]any) { c["onboot"] = 1 }, false},
		{"console mode", func(c map[string]any) { c["cmode"] = "console" }, false},
		{"different generation", func(c map[string]any) { c["description"] = "wrong-owner" }, false},
		{"HDD root", func(c map[string]any) { c["rootfs"] = "pool:subvol-4005-disk-0,size=8G" }, false},
		{"wrong workspace mount", func(c map[string]any) { c["mp0"] = "pool:subvol-4005-disk-1,mp=/other" }, false},
		{"unexpected mount", func(c map[string]any) { c["mp1"] = "/host,mp=/host" }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := map[string]any{"vmid": binding.VMID, "hostname": sessionHostname(binding.SessionID, binding.EpochID, binding.Generation), "description": owner, "unprivileged": 0, "onboot": 0, "cmode": "shell", "rootfs": "local:4005/vm-4005-disk-0.raw,size=8G", "mp0": "pool:subvol-4005-disk-1,mp=/workspace", "net0": "name=eth0,bridge=vmbr0,ip=dhcp,link_down=1"}
			tc.change(config)
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.Method != http.MethodGet || r.URL.Path != "/api2/json/nodes/pve-node/lxc/4005/config" {
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"data": config})
			}))
			defer server.Close()
			runtime, err := NewProxmoxRuntime(ProxmoxConfig{BaseURL: server.URL, Node: "pve-node", Token: "test", Client: server.Client()})
			if err != nil {
				t.Fatal(err)
			}
			evidence, err := runtime.VerifyBootstrapIsolation(context.Background(), binding)
			if tc.valid {
				if err != nil || !validBootstrapEvidence(evidence) {
					t.Fatalf("evidence=%+v err=%v", evidence, err)
				}
			} else if err == nil {
				t.Fatal("unsafe bootstrap snapshot accepted")
			}
			if requests != 1 {
				t.Fatalf("requests=%d; expected one read-only snapshot", requests)
			}
		})
	}
}
