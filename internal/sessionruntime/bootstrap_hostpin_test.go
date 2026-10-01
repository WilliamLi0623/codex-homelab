package sessionruntime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/WilliamLi0623/codex-homelab/internal/store"
)

func newHostPinFixture(t *testing.T, options *bootstrapHostKeyConsoleOptions) (*bootstrapHostPinStage, *SSHMaterialRegistry, store.SessionRuntimeBinding, func()) {
	t.Helper()
	registry, binding := materialRegistryFixture(t)
	options.binding = binding
	runtime, server := newBootstrapHostKeyRuntime(t, options)
	stage, err := newBootstrapHostPinStage(runtime, registry)
	if err != nil {
		server.Close()
		t.Fatalf("newBootstrapHostPinStage() failed: %v", err)
	}
	return stage, registry, binding, server.Close
}

func TestBootstrapHostPinApplyReopenAndObserve(t *testing.T) {
	options := &bootstrapHostKeyConsoleOptions{publicKey: publicMaterialFixture(t)}
	stage, registry, binding, closeServer := newHostPinFixture(t, options)
	defer closeServer()
	if _, err := registry.Prepare(context.Background(), binding); err != nil {
		t.Fatal("prepare fixture material failed")
	}

	evidence, err := stage.Apply(context.Background(), binding)
	if err != nil || !validBootstrapEvidence(evidence) {
		t.Fatalf("Apply() evidence valid=%v error=%v", validBootstrapEvidence(evidence), err)
	}
	reopened, err := NewSSHMaterialRegistry(registry.root, registry.keygen)
	if err != nil {
		t.Fatal("reopen fixture registry failed")
	}
	observer, err := newBootstrapHostPinStage(stage.runtime, reopened)
	if err != nil {
		t.Fatal("reopen host-pin adapter failed")
	}
	observed, verified, err := observer.Observe(context.Background(), binding)
	if err != nil || !verified || observed != evidence {
		t.Fatalf("Observe() verified=%v sameEvidence=%v error=%v", verified, observed == evidence, err)
	}
}

func TestBootstrapHostPinObserveRejectsMissingWrongSubstitutedAndPartialPins(t *testing.T) {
	for _, kind := range []string{"missing", "wrong", "substituted", "partial"} {
		t.Run(kind, func(t *testing.T) {
			consoleKey := publicMaterialFixture(t)
			options := &bootstrapHostKeyConsoleOptions{publicKey: consoleKey}
			stage, registry, binding, closeServer := newHostPinFixture(t, options)
			defer closeServer()
			material, err := registry.Prepare(context.Background(), binding)
			if err != nil {
				t.Fatal("prepare fixture material failed")
			}
			switch kind {
			case "wrong":
				if err := registry.Pin(binding, publicMaterialFixture(t)); err != nil {
					t.Fatal("create wrong fixture pin failed")
				}
			case "substituted":
				if err := registry.Pin(binding, consoleKey); err != nil {
					t.Fatal("create fixture pin failed")
				}
				if err := os.WriteFile(material.KnownHostsFile, []byte(material.Alias+" "+publicMaterialFixture(t)+"\n"), 0600); err != nil {
					t.Fatal("substitute fixture pin failed")
				}
			case "partial":
				if err := os.WriteFile(material.KnownHostsFile, []byte(material.Alias+" "+consoleKey+"\n"), 0600); err != nil {
					t.Fatal("write partial fixture pin failed")
				}
			}
			evidence, verified, err := stage.Observe(context.Background(), binding)
			if err == nil || verified || evidence != (store.SessionBootstrapEvidence{}) {
				t.Fatalf("Observe() evidence=%v verified=%v errorPresent=%v", evidence, verified, err != nil)
			}
			assertHostPinErrorRedacted(t, err, consoleKey)
			if kind == "partial" {
				bytes, readErr := os.ReadFile(material.KnownHostsFile)
				if readErr != nil || string(bytes) != material.Alias+" "+consoleKey+"\n" {
					t.Fatal("Observe() changed the partial known_hosts pin")
				}
				if _, statErr := os.Stat(filepath.Join(filepath.Dir(material.IdentityFile), "pin.sha256")); !os.IsNotExist(statErr) {
					t.Fatal("Observe() created or retained a pin digest marker")
				}
			}
		})
	}
}

func TestBootstrapHostPinApplyRequiresFreshFencedObservation(t *testing.T) {
	for _, kind := range []string{"canceled", "unfenced", "wrong generation"} {
		t.Run(kind, func(t *testing.T) {
			options := &bootstrapHostKeyConsoleOptions{}
			stage, registry, binding, closeServer := newHostPinFixture(t, options)
			defer closeServer()
			if _, err := registry.Prepare(context.Background(), binding); err != nil {
				t.Fatal("prepare fixture material failed")
			}
			ctx := context.Background()
			cancel := func() {}
			switch kind {
			case "canceled":
				var stop context.CancelFunc
				ctx, stop = context.WithCancel(ctx)
				stop()
				cancel = stop
			case "unfenced":
				options.unfenced = true
			case "wrong generation":
				binding.Generation = "other-generation"
			}
			defer cancel()
			evidence, err := stage.Apply(ctx, binding)
			if err == nil || evidence != (store.SessionBootstrapEvidence{}) {
				t.Fatalf("Apply() evidence=%v errorPresent=%v", evidence, err != nil)
			}
			assertHostPinErrorRedacted(t, err, options.publicKey)
			if _, err := registry.Load(store.SessionRuntimeBinding{ID: "binding-a", SessionID: "session-a", EpochID: "epoch-a", Generation: "gen-a", VMID: 4002}); err == nil {
				t.Fatal("failed Apply() left a usable host pin")
			}
		})
	}
}

func TestBootstrapHostPinApplyTwiceDoesNotReplacePin(t *testing.T) {
	stage, registry, binding, closeServer := newHostPinFixture(t, &bootstrapHostKeyConsoleOptions{})
	defer closeServer()
	material, err := registry.Prepare(context.Background(), binding)
	if err != nil {
		t.Fatal("prepare fixture material failed")
	}
	first, err := stage.Apply(context.Background(), binding)
	if err != nil {
		t.Fatalf("first Apply() failed: %v", err)
	}
	before, err := os.ReadFile(material.KnownHostsFile)
	if err != nil {
		t.Fatal("read fixture pin failed")
	}
	second, err := stage.Apply(context.Background(), binding)
	if err == nil || second != (store.SessionBootstrapEvidence{}) {
		t.Fatalf("second Apply() evidence=%v errorPresent=%v", second, err != nil)
	}
	after, readErr := os.ReadFile(material.KnownHostsFile)
	if readErr != nil || string(after) != string(before) {
		t.Fatal("second Apply() changed the exclusive pin")
	}
	if !validBootstrapEvidence(first) {
		t.Fatal("first Apply() did not return evidence")
	}
}

func TestBootstrapHostPinRejectsBindingWithoutID(t *testing.T) {
	stage, _, binding, closeServer := newHostPinFixture(t, &bootstrapHostKeyConsoleOptions{})
	defer closeServer()
	binding.ID = ""
	if evidence, err := stage.Apply(context.Background(), binding); !errors.Is(err, ErrBootstrapBinding) || evidence != (store.SessionBootstrapEvidence{}) {
		t.Fatalf("Apply() evidence=%v error=%v; missing ID must be rejected", evidence, err)
	}
}

func assertHostPinErrorRedacted(t *testing.T, err error, publicKey string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected a safe host-pin error")
	}
	for _, secret := range []string{bootstrapHostKeyTestToken, "ticket-secret", publicKey, "PRIVATE KEY"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatal("host-pin error exposed sensitive material")
		}
	}
}
