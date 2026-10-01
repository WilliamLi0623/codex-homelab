package sessionruntime

import (
	"bytes"
	"strings"
	"testing"

	"github.com/WilliamLi0623/codex-homelab/internal/store"
)

func TestBootstrapGuestIdentityMarkerBindsExactGenerationAndPublicKey(t *testing.T) {
	binding := store.SessionRuntimeBinding{ID: "binding", SessionID: "session", EpochID: "epoch", Generation: "generation", VMID: 4002}
	key := publicMaterialFixture(t)
	data, err := bootstrapGuestIdentityMarkerData(binding, key)
	if err != nil || !bootstrapGuestIdentityMarkerMatches(data, binding, key) {
		t.Fatal("marker did not verify")
	}
	if !bootstrapGuestIdentityMarkerMatches(append(append([]byte(" \n"), data...), '\n'), binding, key) {
		t.Fatal("file newline rejected")
	}
	changed := binding
	changed.Generation = "new"
	if bootstrapGuestIdentityMarkerMatches(data, changed, key) {
		t.Fatal("previous generation marker adopted")
	}
	changed = binding
	changed.RuntimeID = "runtime-not-identity"
	changed.State = "RUNNING"
	if !bootstrapGuestIdentityMarkerMatches(data, changed, key) {
		t.Fatal("runtime lifecycle fields changed immutable identity")
	}
	if bootstrapGuestIdentityMarkerMatches(data, binding, publicMaterialFixture(t)) {
		t.Fatal("another client key accepted")
	}
	for _, mutate := range []func(*store.SessionRuntimeBinding){
		func(b *store.SessionRuntimeBinding) { b.ID = "other" },
		func(b *store.SessionRuntimeBinding) { b.SessionID = "other" },
		func(b *store.SessionRuntimeBinding) { b.EpochID = "other" },
		func(b *store.SessionRuntimeBinding) { b.VMID = 4003 },
	} {
		changed := binding
		mutate(&changed)
		if bootstrapGuestIdentityMarkerMatches(data, changed, key) {
			t.Fatal("another binding accepted")
		}
	}
	if !bytes.Contains(data, []byte(`"version":1`)) || !bytes.Contains(data, []byte(`"client_public_key":`)) {
		t.Fatal("missing fixed marker schema")
	}
}

func TestBootstrapGuestIdentityMarkerRejectsUntrustedPayloads(t *testing.T) {
	binding := store.SessionRuntimeBinding{ID: "binding", SessionID: "session", EpochID: "epoch", Generation: "generation", VMID: 4002}
	key := publicMaterialFixture(t)
	data, err := bootstrapGuestIdentityMarkerData(binding, key)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range [][]byte{nil, []byte("{}"), append(append([]byte(nil), data...), data...), []byte(strings.Repeat(" ", 4097)), bytes.Replace(data, []byte(`"version":1`), []byte(`"version":0,"version":1`), 1), bytes.Replace(data, []byte(`"version":1`), []byte(`"secret":"not-a-real-secret","version":1`), 1)} {
		if bootstrapGuestIdentityMarkerMatches(bad, binding, key) {
			t.Fatal("noncanonical or extra marker payload accepted")
		}
	}
	if _, err := bootstrapGuestIdentityMarkerData(binding, "ssh-ed25519 invalid"); err == nil {
		t.Fatal("invalid client key accepted")
	}
	binding.VMID = 210
	if _, err := bootstrapGuestIdentityMarkerData(binding, key); err == nil {
		t.Fatal("protected guest accepted")
	}
}
