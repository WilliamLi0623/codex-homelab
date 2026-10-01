package sessionruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/WilliamLi0623/codex-homelab/internal/store"
)

func TestProxmoxRuntimeCannotClaimCodexReadinessWithoutBootstrap(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		t.Errorf("unimplemented readiness must not issue bootstrap mutations: %s %s", r.Method, r.URL.Path)
		http.NotFound(w, r)
	}))
	defer server.Close()
	runtime, err := NewProxmoxRuntime(ProxmoxConfig{BaseURL: server.URL, Node: "pve-node", Token: "token", Client: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	err = runtime.CheckReady(context.Background(), store.SessionRuntimeBinding{VMID: 4000, SessionID: "session-a", EpochID: "epoch-a", Generation: "gen-1"})
	if !errors.Is(err, ErrRuntimeNotReady) || requests != 0 {
		t.Fatalf("CheckReady() error=%v requests=%d; Codex bootstrap is not yet verified", err, requests)
	}
}

func TestTermproxyClientFramesUseByteLengthsAndExactControlFormat(t *testing.T) {
	text, err := encodeTermproxyInput("pwd\n")
	if err != nil || string(text) != "0:4:pwd\n" {
		t.Fatalf("encodeTermproxyInput(ASCII) = %q, %v", text, err)
	}

	unicode, err := encodeTermproxyInput("目录\n")
	if err != nil || string(unicode) != "0:7:目录\n" {
		t.Fatalf("encodeTermproxyInput(Unicode) = %q, %v; want UTF-8 byte length 7", unicode, err)
	}

	resize, err := encodeTermproxyResize(120, 40)
	if err != nil || string(resize) != "1:120:40:" {
		t.Fatalf("encodeTermproxyResize() = %q, %v", resize, err)
	}
	if got := string(termproxyPingFrame()); got != "2" {
		t.Fatalf("termproxyPingFrame() = %q, want 2", got)
	}
}

func TestTermproxyResizeRejectsNonPositiveDimensions(t *testing.T) {
	for _, dimensions := range [][2]int{{0, 24}, {80, 0}, {-1, 24}, {80, -1}} {
		if _, err := encodeTermproxyResize(dimensions[0], dimensions[1]); err == nil {
			t.Errorf("encodeTermproxyResize(%d,%d) unexpectedly succeeded", dimensions[0], dimensions[1])
		}
	}
}

func TestProxmoxCloneUsesSessionRangeSSDAndDHCP(t *testing.T) {
	var cloneForm url.Values
	var dhcpForm url.Values
	workspaceAttached := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "PVEAPIToken=test-token" {
			t.Errorf("Authorization = %q, want configured API token", r.Header.Get("Authorization"))
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api2/json/nodes/pve-node/lxc/3900/config":
			_, _ = w.Write([]byte(`{"data":{"template":1}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api2/json/nodes/pve-node/lxc/3900/clone":
			if err := r.ParseForm(); err != nil {
				t.Errorf("ParseForm: %v", err)
			}
			cloneForm = r.Form
			_, _ = w.Write([]byte(`{"data":"UPID:node:task"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api2/json/nodes/pve-node/tasks/UPID:node:task/status":
			_, _ = w.Write([]byte(`{"data":{"status":"stopped","exitstatus":"OK"}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api2/json/nodes/pve-node/lxc/4000/config":
			if workspaceAttached {
				_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"net0": dhcpForm.Get("net0"), "cmode": dhcpForm.Get("cmode"), "onboot": 0, "mp0": "pool:subvol-4000-disk-1,mp=/workspace,backup=1"}})
			} else {
				_, _ = w.Write([]byte(`{"data":{"net0":"name=eth0,bridge=vmbr0,ip=192.0.2.20/24,tag=20"}}`))
			}
		case r.Method == http.MethodPut && r.URL.Path == "/api2/json/nodes/pve-node/lxc/4000/config":
			if err := r.ParseForm(); err != nil {
				t.Errorf("ParseForm: %v", err)
			}
			dhcpForm = r.Form
			if r.Form.Get("mp0") != "pool:32,mp=/workspace,backup=1" {
				t.Errorf("mp0 = %q, want workspace volume allocation on pool", r.Form.Get("mp0"))
			}
			workspaceAttached = true
			_, _ = w.Write([]byte(`{"data":null}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	runtime, err := NewProxmoxRuntime(ProxmoxConfig{BaseURL: server.URL, Node: "pve-node", Token: "PVEAPIToken=test-token", Client: server.Client()})
	if err != nil {
		t.Fatalf("NewProxmoxRuntime() error = %v", err)
	}
	workspaceVolumeID, err := runtime.Clone(context.Background(), RuntimeRequest{VMID: 4000, SessionID: "session-a", EpochID: "epoch-a", Generation: "gen-1", TemplateVMID: 3900, SystemStorage: "local", WorkspaceStorage: "pool", WorkspaceSizeGiB: 32, Hostname: "codex-session-123"})
	if err != nil {
		t.Fatalf("Clone() error = %v", err)
	}
	if workspaceVolumeID != "pool:subvol-4000-disk-1" {
		t.Fatalf("Clone() workspace volume = %q", workspaceVolumeID)
	}
	if cloneForm.Get("newid") != "4000" || cloneForm.Get("storage") != "local" || cloneForm.Get("full") != "1" || cloneForm.Get("hostname") != "codex-session-123" {
		t.Fatalf("clone form = %v, want dedicated VMID and SSD root storage", cloneForm)
	}
	if cloneForm.Get("pool") != "codex-sessions" {
		t.Fatalf("clone pool = %q, want dedicated Session pool codex-sessions", cloneForm.Get("pool"))
	}
	if !strings.HasPrefix(cloneForm.Get("description"), "codex-session:v1:") {
		t.Fatalf("clone ownership metadata = %q", cloneForm.Get("description"))
	}
	if dhcpForm.Get("net0") != "name=eth0,bridge=vmbr0,ip=dhcp,tag=20,link_down=1" {
		t.Fatalf("DHCP network config = %q", dhcpForm.Get("net0"))
	}
	if dhcpForm.Get("onboot") != "0" || dhcpForm.Get("cmode") != "shell" {
		t.Fatalf("unsafe bootstrap startup/console config: %v", dhcpForm)
	}
}

func TestSessionOwnershipAcceptsOnlyOneTerminalPVELineEnding(t *testing.T) {
	encoded, err := encodeOwnership(sessionOwnership{Version: 1, SessionID: "s", EpochID: "e", Generation: "g"})
	if err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", "\n", "\r\n"} {
		var owner sessionOwnership
		if err := decodeOwnership(encoded+suffix, &owner); err != nil || owner.SessionID != "s" {
			t.Errorf("PVE suffix %q: owner=%+v error=%v", suffix, owner, err)
		}
	}
	for _, description := range []string{" " + encoded, encoded + " ", encoded + "\n\n", encoded + "\r", encoded[:20] + "\n" + encoded[20:], encoded + "\nother"} {
		var owner sessionOwnership
		if err := decodeOwnership(description, &owner); err == nil {
			t.Errorf("accepted malformed description %q", description)
		}
	}
}

func TestDHCPBootstrapFencesExistingAndAdditionalInterfaces(t *testing.T) {
	for _, test := range []struct{ input, want string }{
		{"name=eth0,ip=10.0.0.1/24,link_down=0,tag=20", "name=eth0,ip=dhcp,link_down=1,tag=20"},
		{"name=eth1,bridge=vmbr0", "name=eth1,bridge=vmbr0,ip=dhcp,link_down=1"},
	} {
		if got := withDHCP(test.input); got != test.want {
			t.Errorf("network %q: got %q want %q", test.input, got, test.want)
		}
	}
}

func TestBootstrapIsolationReadbackFailureIsUnknownAndNeverReplayed(t *testing.T) {
	for _, fault := range []string{"net0", "cmode", "onboot", "unobserved-interface", "read-error"} {
		t.Run(fault, func(t *testing.T) {
			puts := 0
			config := map[string]any{"net0": "name=eth0,bridge=vmbr0,ip=dhcp", "net1": "name=eth1,bridge=vmbr0,link_down=0"}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api2/json/nodes/pve-node/lxc/4001/config" {
					t.Errorf("unexpected path %s", r.URL.Path)
					http.NotFound(w, r)
					return
				}
				if r.Method == http.MethodPut {
					puts++
					if err := r.ParseForm(); err != nil {
						t.Error(err)
					}
					if r.Form.Get("net1") != "name=eth1,bridge=vmbr0,link_down=1,ip=dhcp" {
						t.Errorf("second NIC not fenced: %v", r.Form)
					}
					config["net0"], config["net1"], config["cmode"], config["onboot"] = r.Form.Get("net0"), r.Form.Get("net1"), r.Form.Get("cmode"), 0
					config["mp0"] = "pool:subvol-4001-disk-1,mp=/workspace,backup=1"
					switch fault {
					case "onboot":
						config["onboot"] = 1
					case "net0":
						config["net0"] = "name=eth0,bridge=vmbr0,ip=dhcp,link_down=0"
					case "cmode":
						config["cmode"] = "tty"
					case "unobserved-interface":
						config["net2"] = "name=eth2,bridge=vmbr0,ip=dhcp,link_down=0"
					}
					_, _ = w.Write([]byte(`{"data":null}`))
					return
				}
				if puts > 0 && fault == "read-error" {
					http.Error(w, "unavailable", http.StatusServiceUnavailable)
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"data": config})
			}))
			defer server.Close()
			runtime, err := NewProxmoxRuntime(ProxmoxConfig{BaseURL: server.URL, Node: "pve-node", Token: "token", Client: server.Client()})
			if err != nil {
				t.Fatal(err)
			}
			err = runtime.configureNetworkAndWorkspace(context.Background(), RuntimeRequest{VMID: 4001, WorkspaceStorage: "pool", WorkspaceSizeGiB: 8})
			if !errors.Is(err, ErrOutcomeUnknown) || puts != 1 {
				t.Fatalf("error=%v puts=%d; want UNKNOWN without mutation replay", err, puts)
			}
		})
	}
}

func TestProxmoxTargetAvailabilityAndRuntimeRangeAreIsolated(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api2/json/cluster/nextid" || r.URL.Query().Get("vmid") != "4000" {
			t.Errorf("unexpected request %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"errors":{"vmid":"VM 4000 already exists"}}`))
	}))
	defer server.Close()
	runtime, err := NewProxmoxRuntime(ProxmoxConfig{BaseURL: server.URL, Node: "pve-node", Token: "token", Client: server.Client()})
	if err != nil {
		t.Fatalf("NewProxmoxRuntime() error = %v", err)
	}
	if available, err := runtime.TargetAvailable(context.Background(), 4000); err != nil || available {
		t.Fatalf("TargetAvailable(4000) = %v, %v; want occupied", available, err)
	}
	if _, err := runtime.TargetAvailable(context.Background(), 3010); err == nil {
		t.Fatal("worker VMID was accepted by Session runtime adapter")
	}
}

func TestProxmoxRuntimeRejectsInsecureRemoteHTTP(t *testing.T) {
	if _, err := NewProxmoxRuntime(ProxmoxConfig{BaseURL: "http://pve.example.invalid:8006", Node: "pve-node", Token: "secret"}); err == nil {
		t.Fatal("Proxmox API token would be sent to a non-loopback HTTP endpoint")
	}
}

func TestProxmoxCloneRequiresConfiguredSourceToBeATemplate(t *testing.T) {
	cloneCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api2/json/nodes/pve-node/lxc/3900/config" {
			_, _ = w.Write([]byte(`{"data":{"template":0}}`))
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == "/api2/json/nodes/pve-node/lxc/3900/clone" {
			cloneCalls++
		}
		t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		http.NotFound(w, r)
	}))
	defer server.Close()
	runtime, err := NewProxmoxRuntime(ProxmoxConfig{BaseURL: server.URL, Node: "pve-node", Token: "token", Client: server.Client()})
	if err != nil {
		t.Fatalf("NewProxmoxRuntime() error = %v", err)
	}
	_, err = runtime.Clone(context.Background(), RuntimeRequest{VMID: 4000, SessionID: "session-a", EpochID: "epoch-a", Generation: "gen-1", TemplateVMID: 3900, SystemStorage: "local", WorkspaceStorage: "pool", WorkspaceSizeGiB: 32, Hostname: "codex-session-123"})
	if err == nil {
		t.Fatal("Clone() succeeded from a non-template LXC")
	}
	if cloneCalls != 0 {
		t.Fatalf("clone API calls = %d, want zero for a non-template source", cloneCalls)
	}
}

func TestObserveDoesNotTreatAuthorizationFailureAsMissingRuntime(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api2/json/nodes/pve-node/lxc/4002/status/current":
			http.Error(w, "permission denied", http.StatusForbidden)
		case "/api2/json/cluster/resources":
			_, _ = w.Write([]byte(`{"data":[]}`))
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	runtime, err := NewProxmoxRuntime(ProxmoxConfig{BaseURL: server.URL, Node: "pve-node", Token: "token", Client: server.Client()})
	if err != nil {
		t.Fatalf("NewProxmoxRuntime() error = %v", err)
	}
	state, err := runtime.Observe(context.Background(), 4002)
	if err == nil || state == RuntimeMissing {
		t.Fatalf("Observe() = %s, %v; authorization failure must not imply missing runtime", state, err)
	}
}

func TestProxmoxDeleteUsesDELETEAndIdentityRequiresExactOwnership(t *testing.T) {
	deleteMethod := ""
	unprivileged := 0
	const vmid = 4001
	owner, err := encodeOwnership(sessionOwnership{Version: 1, SessionID: "session-a", EpochID: "epoch-a", Generation: "gen-1"})
	if err != nil {
		t.Fatalf("encodeOwnership() error = %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api2/json/nodes/pve-node/lxc/4001/config" && r.Method == http.MethodGet:
			data, _ := json.Marshal(map[string]any{"data": map[string]any{"vmid": vmid, "hostname": sessionHostname("session-a", "epoch-a", "gen-1"), "description": owner, "unprivileged": unprivileged, "rootfs": "local:4001/vm-4001-disk-0.raw,size=8G"}})
			_, _ = w.Write(data)
		case r.URL.Path == "/api2/json/nodes/pve-node/lxc/4001" && r.Method == http.MethodDelete:
			deleteMethod = r.Method
			_, _ = w.Write([]byte(`{"data":"UPID:node:delete"}`))
		case r.URL.Path == "/api2/json/nodes/pve-node/tasks/UPID:node:delete/status":
			_, _ = w.Write([]byte(`{"data":{"status":"stopped","exitstatus":"OK"}}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	runtime, err := NewProxmoxRuntime(ProxmoxConfig{BaseURL: server.URL, Node: "pve-node", Token: "token", Client: server.Client()})
	if err != nil {
		t.Fatalf("NewProxmoxRuntime() error = %v", err)
	}
	binding := store.SessionRuntimeBinding{VMID: vmid, SessionID: "session-a", EpochID: "epoch-a", Generation: "gen-1"}
	if err := runtime.VerifyIdentity(context.Background(), binding); err != nil {
		t.Fatalf("VerifyIdentity() error = %v", err)
	}
	unprivileged = 1
	if err := runtime.VerifyIdentity(context.Background(), binding); err == nil {
		t.Fatal("VerifyIdentity() accepted unprivileged Session LXC")
	}
	unprivileged = 0
	if err := runtime.Delete(context.Background(), vmid); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if deleteMethod != http.MethodDelete {
		t.Fatalf("delete HTTP method = %q", deleteMethod)
	}
	if err := runtime.VerifyIdentity(context.Background(), store.SessionRuntimeBinding{VMID: vmid, SessionID: "other-session", EpochID: "epoch-a", Generation: "gen-1"}); err == nil {
		t.Fatal("VerifyIdentity() accepted mismatched Session owner")
	}
}

func TestProxmoxIdentityRejectsRootDiskOutsideSSDStorage(t *testing.T) {
	for _, rootfs := range []string{"", "pool:subvol-4001-disk-0,size=8G", "/pool/session-rootfs", "local-other:disk"} {
		t.Run(rootfs, func(t *testing.T) {
			owner, err := encodeOwnership(sessionOwnership{Version: 1, SessionID: "session-a", EpochID: "epoch-a", Generation: "gen-1"})
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Fatalf("identity verification issued %s", r.Method)
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"vmid": 4001, "hostname": sessionHostname("session-a", "epoch-a", "gen-1"), "description": owner, "rootfs": rootfs}})
			}))
			defer server.Close()
			runtime, err := NewProxmoxRuntime(ProxmoxConfig{BaseURL: server.URL, Node: "pve-node", Token: "token", Client: server.Client()})
			if err != nil {
				t.Fatal(err)
			}
			if err := runtime.VerifyIdentity(context.Background(), store.SessionRuntimeBinding{VMID: 4001, SessionID: "session-a", EpochID: "epoch-a", Generation: "gen-1"}); err == nil {
				t.Fatalf("VerifyIdentity() accepted rootfs %q outside required SSD storage", rootfs)
			}
		})
	}
}

func TestProxmoxIdentityRejectsEachMismatchedEpochOwnershipField(t *testing.T) {
	for _, owner := range []sessionOwnership{
		{Version: 2, SessionID: "session-a", EpochID: "epoch-a", Generation: "gen-1"},
		{Version: 1, SessionID: "other-session", EpochID: "epoch-a", Generation: "gen-1"},
		{Version: 1, SessionID: "session-a", EpochID: "other-epoch", Generation: "gen-1"},
		{Version: 1, SessionID: "session-a", EpochID: "epoch-a", Generation: "other-generation"},
	} {
		t.Run(fmt.Sprintf("%d-%s-%s-%s", owner.Version, owner.SessionID, owner.EpochID, owner.Generation), func(t *testing.T) {
			description, err := encodeOwnership(owner)
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"vmid": 4001, "hostname": sessionHostname("session-a", "epoch-a", "gen-1"), "description": description, "rootfs": "local:4001/vm-4001-disk-0.raw,size=8G"}})
			}))
			defer server.Close()
			runtime, err := NewProxmoxRuntime(ProxmoxConfig{BaseURL: server.URL, Node: "pve-node", Token: "token", Client: server.Client()})
			if err != nil {
				t.Fatal(err)
			}
			if err := runtime.VerifyIdentity(context.Background(), store.SessionRuntimeBinding{VMID: 4001, SessionID: "session-a", EpochID: "epoch-a", Generation: "gen-1"}); err == nil {
				t.Fatal("VerifyIdentity() accepted mismatched epoch ownership")
			}
		})
	}
}

func TestDetachWorkspaceVerifiesUnusedVolumeAndKeepsItOnPool(t *testing.T) {
	attached := true
	const volumeID = "pool:subvol-4003-disk-1"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api2/json/nodes/pve-node/lxc/4003/config":
			if attached {
				_, _ = w.Write([]byte(`{"data":{"mp0":"pool:subvol-4003-disk-1,mp=/workspace,backup=1"}}`))
			} else {
				_, _ = w.Write([]byte(`{"data":{"unused0":"pool:subvol-4003-disk-1"}}`))
			}
		case r.Method == http.MethodPut && r.URL.Path == "/api2/json/nodes/pve-node/lxc/4003/config":
			if err := r.ParseForm(); err != nil {
				t.Errorf("ParseForm: %v", err)
			}
			if r.Form.Get("delete") != "mp0" {
				t.Errorf("delete config = %v, want delete=mp0", r.Form)
			}
			attached = false
			_, _ = w.Write([]byte(`{"data":null}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api2/json/nodes/pve-node/storage/pool/content":
			if r.URL.Query().Get("content") != "rootdir" {
				t.Errorf("storage content filter = %q", r.URL.Query().Get("content"))
			}
			_, _ = w.Write([]byte(`{"data":[{"volid":"pool:subvol-4003-disk-1"}]}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	runtime, err := NewProxmoxRuntime(ProxmoxConfig{BaseURL: server.URL, Node: "pve-node", Token: "token", Client: server.Client()})
	if err != nil {
		t.Fatalf("NewProxmoxRuntime() error = %v", err)
	}
	if err := runtime.DetachWorkspace(context.Background(), 4003, volumeID); err != nil {
		t.Fatalf("DetachWorkspace() error = %v", err)
	}
	if exists, err := runtime.WorkspaceVolumeExists(context.Background(), volumeID); err != nil || !exists {
		t.Fatalf("WorkspaceVolumeExists() = %v, %v; want preserved workspace volume", exists, err)
	}
}

func TestDetachWorkspaceRejectsSimilarButDifferentMountPath(t *testing.T) {
	putCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api2/json/nodes/pve-node/lxc/4004/config" {
			_, _ = w.Write([]byte(`{"data":{"mp0":"pool:subvol-4004-disk-1,mp=/workspace-other,backup=1"}}`))
			return
		}
		if r.Method == http.MethodPut && r.URL.Path == "/api2/json/nodes/pve-node/lxc/4004/config" {
			putCalls++
		}
		t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		http.NotFound(w, r)
	}))
	defer server.Close()
	runtime, err := NewProxmoxRuntime(ProxmoxConfig{BaseURL: server.URL, Node: "pve-node", Token: "token", Client: server.Client()})
	if err != nil {
		t.Fatalf("NewProxmoxRuntime() error = %v", err)
	}
	err = runtime.DetachWorkspace(context.Background(), 4004, "pool:subvol-4004-disk-1")
	if err == nil {
		t.Fatal("DetachWorkspace() accepted a mount path other than /workspace")
	}
	if putCalls != 0 {
		t.Fatalf("workspace detach API calls = %d, want zero for a mismatched mount path", putCalls)
	}
}
