package capacity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestProxmoxRuntimeCloneUsesOnlyDynamicVMIDs(t *testing.T) {
	var gotPath string
	var gotAuth string
	var gotForm url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if strings.Contains(r.URL.Path, "/tasks/") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"data":{"status":"stopped","exitstatus":"OK"}}`)
			return
		}
		gotPath = r.URL.Path
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
		Range:   VMIDRange{Min: 3000, Max: 3899},
		Client:  server.Client(),
	})

	node, err := runtime.Create(context.Background(), CreateRequest{VMID: 3010, TemplateVMID: 3900, Generation: "gen-1", Hostname: "codex-3010", Metadata: map[string]string{"managed-by": ManagedBy, "task": "task-1"}})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if node.State != NodeCreating || node.VMID != 3010 {
		t.Fatalf("created node = %+v", node)
	}
	if gotPath != "/api2/json/nodes/pve-node/lxc/3900/clone" {
		t.Fatalf("clone path = %q", gotPath)
	}
	if gotAuth == "" {
		t.Fatal("clone request omitted injected authorization")
	}
	if gotForm.Get("newid") != "3010" || gotForm.Get("hostname") != "codex-3010" || gotForm.Get("full") != "1" || gotForm.Get("pool") != "codex-workers" || gotForm.Get("description") != "managed-by=codex-homelab\ntask=task-1" {
		t.Fatalf("clone form = %v", gotForm)
	}
}

func TestProxmoxRuntimeDoesNotMarkRejectedCloneAsUnknown(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api2/json/nodes/pve-node/lxc/3900/clone" {
			t.Fatalf("clone path = %q", r.URL.Path)
		}
		http.Error(w, "permission denied", http.StatusForbidden)
	}))
	defer server.Close()

	runtime := NewProxmoxRuntime(ProxmoxConfig{BaseURL: server.URL, Node: "pve-node", Token: "secret", Range: VMIDRange{Min: 3000, Max: 3899}, Client: server.Client()})
	_, err := runtime.Create(context.Background(), CreateRequest{VMID: 3010, TemplateVMID: 3900, Generation: "gen-1", Hostname: "codex-3010"})
	if !errors.Is(err, ErrRejected) {
		t.Fatalf("Create() error = %v, want ErrRejected", err)
	}
	if errors.Is(err, ErrUnknown) {
		t.Fatalf("Create() error = %v, must not be ErrUnknown", err)
	}
}

func TestProxmoxRuntimeClassifiesTemplateDiskLockAsRejected(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api2/json/nodes/pve-node/lxc/3900/clone" {
			t.Fatalf("clone path = %q", r.URL.Path)
		}
		http.Error(w, "CT is locked (disk)", http.StatusInternalServerError)
	}))
	defer server.Close()

	runtime := NewProxmoxRuntime(ProxmoxConfig{BaseURL: server.URL, Node: "pve-node", Token: "secret", Range: VMIDRange{Min: 3000, Max: 3899}, Client: server.Client()})
	_, err := runtime.Create(context.Background(), CreateRequest{VMID: 3010, TemplateVMID: 3900, Generation: "gen-1", Hostname: "codex-3010"})
	if !errors.Is(err, ErrRejected) {
		t.Fatalf("Create() error = %v, want ErrRejected", err)
	}
	if errors.Is(err, ErrUnknown) {
		t.Fatalf("Create() error = %v, must not be ErrUnknown", err)
	}
}

func TestProxmoxRuntimeSerializesTemplateClones(t *testing.T) {
	firstTaskObserved := make(chan struct{})
	allowFirstTaskToFinish := make(chan struct{})
	var finishOnce sync.Once
	finishFirstTask := func() { finishOnce.Do(func() { close(allowFirstTaskToFinish) }) }
	secondCloneObserved := make(chan struct{}, 1)
	var cloneCount int
	var activeClones int
	var maxActiveClones int
	var lock sync.Mutex

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/clone") {
			lock.Lock()
			cloneCount++
			taskID := cloneCount
			activeClones++
			if activeClones > maxActiveClones {
				maxActiveClones = activeClones
			}
			lock.Unlock()
			if taskID == 2 {
				secondCloneObserved <- struct{}{}
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"data":"UPID:%d"}`, taskID)
			return
		}
		if strings.Contains(r.URL.Path, "/tasks/") {
			if strings.Contains(r.URL.Path, "UPID:1") {
				select {
				case <-firstTaskObserved:
				default:
					close(firstTaskObserved)
				}
				<-allowFirstTaskToFinish
			}
			lock.Lock()
			activeClones--
			lock.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"data":{"status":"stopped","exitstatus":"OK"}}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	defer finishFirstTask()

	runtime := NewProxmoxRuntime(ProxmoxConfig{BaseURL: server.URL, Node: "pve-node", Token: "secret", Range: VMIDRange{Min: 3000, Max: 3899}, Client: server.Client()})
	create := func(vmid int) error {
		_, err := runtime.Create(context.Background(), CreateRequest{VMID: vmid, TemplateVMID: 3900, Generation: fmt.Sprintf("gen-%d", vmid), Hostname: fmt.Sprintf("codex-%d", vmid)})
		return err
	}
	firstResult := make(chan error, 1)
	go func() { firstResult <- create(3010) }()
	select {
	case <-firstTaskObserved:
	case <-time.After(time.Second):
		t.Fatal("first clone did not reach task observation")
	}
	secondResult := make(chan error, 1)
	secondStarted := make(chan struct{})
	go func() {
		close(secondStarted)
		secondResult <- create(3011)
	}()
	<-secondStarted
	select {
	case <-secondCloneObserved:
		finishFirstTask()
		t.Fatal("second clone started while the first template clone was still running")
	case <-time.After(100 * time.Millisecond):
	}
	finishFirstTask()
	for name, result := range map[string]<-chan error{"first": firstResult, "second": secondResult} {
		select {
		case err := <-result:
			if err != nil {
				t.Fatalf("%s Create() error = %v", name, err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("%s clone did not complete", name)
		}
	}
	lock.Lock()
	defer lock.Unlock()
	if cloneCount != 2 || maxActiveClones != 1 {
		t.Fatalf("cloneCount=%d maxActiveClones=%d, want 2 and 1", cloneCount, maxActiveClones)
	}
}

func TestProxmoxRuntimeClassifiesExistingTargetAsOccupied(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api2/json/nodes/pve-node/lxc/3900/clone" {
			t.Fatalf("clone path = %q", r.URL.Path)
		}
		http.Error(w, "CT 3010 already exists on node 'pve-node'", http.StatusInternalServerError)
	}))
	defer server.Close()

	runtime := NewProxmoxRuntime(ProxmoxConfig{BaseURL: server.URL, Node: "pve-node", Token: "secret", Range: VMIDRange{Min: 3000, Max: 3899}, Client: server.Client()})
	_, err := runtime.Create(context.Background(), CreateRequest{VMID: 3010, TemplateVMID: 3900, Generation: "gen-1", Hostname: "codex-3010"})
	if !errors.Is(err, ErrVMIDOccupied) {
		t.Fatalf("Create() error = %v, want ErrVMIDOccupied", err)
	}
	if errors.Is(err, ErrUnknown) {
		t.Fatalf("Create() error = %v, must not be ErrUnknown", err)
	}
}

func TestProxmoxRuntimeRejectsProtectedActionBeforeHTTP(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer server.Close()

	runtime := NewProxmoxRuntime(ProxmoxConfig{BaseURL: server.URL, Node: "pve-node", Range: VMIDRange{Min: 3000, Max: 3899}, Client: server.Client()})
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

	runtime := NewProxmoxRuntime(ProxmoxConfig{BaseURL: server.URL, Node: "pve-node", Token: "secret", Range: VMIDRange{Min: 3000, Max: 3899}, Client: server.Client()})
	node, err := runtime.Observe(context.Background(), 3010)
	if err != nil {
		t.Fatalf("Observe() error = %v", err)
	}
	if node.VMID != 3010 || node.State != NodeRunning || node.KubeNode != "codex-3010" {
		t.Fatalf("observed node = %+v", node)
	}
}

func TestProxmoxRuntimeTreatsMissingConfigAsAbsentTarget(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api2/json/nodes/pve-node/lxc/3012/status/current" {
			t.Fatalf("status path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"data":null,"message":"Configuration file 'nodes/pve-node/lxc/3012.conf' does not exist\n"}`)
	}))
	defer server.Close()

	runtime := NewProxmoxRuntime(ProxmoxConfig{BaseURL: server.URL, Node: "pve-node", Token: "secret", Range: VMIDRange{Min: 3000, Max: 3899}, Client: server.Client()})
	_, err := runtime.Observe(context.Background(), 3012)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Observe() error = %v, want ErrNotFound", err)
	}
}

func TestProxmoxRuntimeTargetAvailableUsesClusterInventory(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api2/json/cluster/resources" || r.URL.Query().Get("type") != "vm" {
			t.Fatalf("inventory request = %s %s", r.Method, r.URL.RequestURI())
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[{"type":"lxc","vmid":3013,"name":"template"},{"type":"lxc","vmid":3011,"name":"worker"}]}`)
	}))
	defer server.Close()

	runtime := NewProxmoxRuntime(ProxmoxConfig{BaseURL: server.URL, Node: "pve-node", Token: "secret", Range: VMIDRange{Min: 3000, Max: 3899}, Client: server.Client()})
	available, err := runtime.TargetAvailable(context.Background(), 3014)
	if err != nil || !available {
		t.Fatalf("TargetAvailable(absent) = (%t, %v), want true, nil", available, err)
	}
	available, err = runtime.TargetAvailable(context.Background(), 3013)
	if err != nil || available {
		t.Fatalf("TargetAvailable(present) = (%t, %v), want false, nil", available, err)
	}
}

func TestProxmoxRuntimeStopTreatsAlreadyStoppedAsIdempotent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api2/json/nodes/pve-node/lxc/3010/status/current" {
			t.Fatalf("unexpected stop request = %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":{"vmid":3010,"name":"codex-3010","status":"stopped"}}`)
	}))
	defer server.Close()

	runtime := NewProxmoxRuntime(ProxmoxConfig{BaseURL: server.URL, Node: "pve-node", Token: "secret", Range: VMIDRange{Min: 3000, Max: 3899}, Client: server.Client()})
	if err := runtime.Stop(context.Background(), 3010); err != nil {
		t.Fatalf("Stop(already stopped) error = %v", err)
	}
}

func TestProxmoxRuntimeVerifiesExactDynamicIdentityReadOnly(t *testing.T) {
	metadata, err := WorkerMetadata("gen-1", "task-1", "2026-09-20T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	description, err := EncodeMetadata(metadata)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api2/json/nodes/pve-node/lxc/3010/config" {
			t.Fatalf("identity request = %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"hostname": "codex-lxc-3010-gen-1", "description": description}})
	}))
	defer server.Close()
	runtime := NewProxmoxRuntime(ProxmoxConfig{BaseURL: server.URL, Node: "pve-node", Token: "secret", Range: VMIDRange{Min: 3000, Max: 3899}, Client: server.Client()})
	if err := runtime.VerifyIdentity(context.Background(), Node{VMID: 3010, Generation: "gen-1", TaskID: "task-1"}); err != nil {
		t.Fatalf("VerifyIdentity() error = %v", err)
	}
}

func TestProxmoxRuntimeRejectsMismatchedIdentity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"vmid": 3011, "hostname": "codex-lxc-3010-gen-1", "description": "managed-by=codex-homelab"}})
	}))
	defer server.Close()
	runtime := NewProxmoxRuntime(ProxmoxConfig{BaseURL: server.URL, Node: "pve-node", Token: "secret", Range: VMIDRange{Min: 3000, Max: 3899}, Client: server.Client()})
	if err := runtime.VerifyIdentity(context.Background(), Node{VMID: 3010, Generation: "gen-1", TaskID: "task-1"}); !errors.Is(err, ErrIdentityInvalid) || !strings.Contains(err.Error(), "identity fields mismatch") {
		t.Fatalf("VerifyIdentity() error = %v, want ErrIdentityInvalid", err)
	}
}
