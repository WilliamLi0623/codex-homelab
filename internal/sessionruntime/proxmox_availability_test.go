package sessionruntime

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTargetAvailabilityUsesGlobalCheckDespiteHiddenInventory(t *testing.T) {
	nextIDCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api2/json/cluster/resources" {
			_, _ = w.Write([]byte(`{"data":[]}`)) // occupied guest is hidden by permissions
			return
		}
		if r.Method != http.MethodGet || r.URL.Path != "/api2/json/cluster/nextid" || r.URL.Query().Get("vmid") != "4000" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		nextIDCalls++
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"errors":{"vmid":"VM 4000 already exists"},"message":"Parameter verification failed.\n","data":null}`))
	}))
	defer server.Close()
	runtime, err := NewProxmoxRuntime(ProxmoxConfig{BaseURL: server.URL, Node: "node", Token: "token", Client: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	available, err := runtime.TargetAvailable(context.Background(), 4000)
	if err != nil || available || nextIDCalls != 1 {
		t.Fatalf("TargetAvailable() = %v, %v, global checks=%d; hidden guest must remain occupied", available, err, nextIDCalls)
	}
}

func TestGlobalAvailabilityRejectsUncertainResponses(t *testing.T) {
	for _, test := range []struct {
		name      string
		status    int
		body      string
		available bool
		wantError bool
	}{
		{"integer", 200, `{"data":4001}`, true, false},
		{"decimal string", 200, `{"data":"4001"}`, true, false},
		{"occupied", 400, `{"errors":{"vmid":"VM 4001 already exists"}}`, false, false},
		{"other invalid request", 400, `{"errors":{"vmid":"invalid VMID"}}`, false, true},
		{"wrong collision ID", 400, `{"errors":{"vmid":"VM 4000 already exists"}}`, false, true},
		{"malformed collision", 400, `not-json VM 4001 already exists`, false, true},
		{"authentication", 401, `{"data":null}`, false, true},
		{"authorization", 403, `{"data":null}`, false, true},
		{"missing endpoint", 404, `{"data":null}`, false, true},
		{"upstream failure", 500, `{"data":null}`, false, true},
		{"wrong returned ID", 200, `{"data":4002}`, false, true},
		{"missing ID", 200, `{}`, false, true},
		{"null", 200, `{"data":null}`, false, true},
		{"boolean", 200, `{"data":true}`, false, true},
		{"fraction", 200, `{"data":4001.5}`, false, true},
		{"exponent", 200, `{"data":4.001e3}`, false, true},
		{"malformed JSON", 200, `{`, false, true},
		{"trailing garbage", 200, `{"data":4001} trailing-garbage`, false, true},
		{"second JSON value", 200, `{"data":4001}{"data":4002}`, false, true},
		{"oversized response", 200, `{"data":4001}` + strings.Repeat(" ", 1<<20), false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/api2/json/cluster/nextid" || r.URL.Query().Get("vmid") != "4001" {
					t.Errorf("unexpected availability request %s %s", r.Method, r.URL.Path)
				}
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			runtime, err := NewProxmoxRuntime(ProxmoxConfig{BaseURL: server.URL, Node: "node", Token: "token", Client: server.Client()})
			if err != nil {
				t.Fatal(err)
			}
			available, err := runtime.TargetAvailable(context.Background(), 4001)
			if available != test.available || (err != nil) != test.wantError {
				t.Fatalf("TargetAvailable() = %v, %v; want available=%v error=%v", available, err, test.available, test.wantError)
			}
		})
	}
}

func TestObserveDoesNotTreatHiddenOccupiedGuestAsMissing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api2/json/nodes/node/lxc/4000/status/current":
			http.Error(w, "guest status unavailable", http.StatusInternalServerError)
		case "/api2/json/cluster/resources":
			_, _ = w.Write([]byte(`{"data":[]}`))
		case "/api2/json/cluster/nextid":
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"errors":{"vmid":"VM 4000 already exists"}}`))
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	runtime, err := NewProxmoxRuntime(ProxmoxConfig{BaseURL: server.URL, Node: "node", Token: "token", Client: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	state, err := runtime.Observe(context.Background(), 4000)
	if err == nil || state != RuntimeUnknown {
		t.Fatalf("Observe() = %s, %v; must not infer absence from filtered inventory", state, err)
	}
}

func TestMalformedCloneAcknowledgmentRemainsUnknownWithoutReplay(t *testing.T) {
	cloneCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api2/json/nodes/node/lxc/3900/config":
			_, _ = w.Write([]byte(`{"data":{"template":1}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api2/json/nodes/node/lxc/3900/clone":
			cloneCalls++
			_, _ = w.Write([]byte(`{"data":"UPID:node:clone"} trailing-garbage`))
		default:
			t.Errorf("unexpected follow-up request after ambiguous clone: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	runtime, err := NewProxmoxRuntime(ProxmoxConfig{BaseURL: server.URL, Node: "node", Token: "token", Client: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runtime.Clone(context.Background(), RuntimeRequest{
		VMID: 4001, TemplateVMID: 3900, SessionID: "session", EpochID: "epoch", Generation: "generation",
		SystemStorage: "local", WorkspaceStorage: "pool", WorkspaceSizeGiB: 8, Hostname: "codex-session-test",
	})
	if !errors.Is(err, ErrOutcomeUnknown) || cloneCalls != 1 {
		t.Fatalf("Clone() error=%v calls=%d; malformed acknowledgment must not permit replay", err, cloneCalls)
	}
}
