package sessionruntime

import (
	"testing"

	"github.com/WilliamLi0623/codex-homelab/internal/store"
)

func TestBootstrapGuestIdentityEvidenceBindsGenerationKeyAndFence(t *testing.T) {
	binding := store.SessionRuntimeBinding{ID: "binding", SessionID: "session", EpochID: "epoch", Generation: "generation", VMID: 4002}
	key := publicMaterialFixture(t)
	fence := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	first, err := bootstrapGuestIdentityEvidence(binding, key, fence)
	if err != nil || !validBootstrapEvidence(first) {
		t.Fatalf("evidence=%+v err=%v", first, err)
	}
	changed := binding
	changed.Generation = "next-generation"
	second, err := bootstrapGuestIdentityEvidence(changed, key, fence)
	if err != nil || first == second {
		t.Fatalf("generation did not alter evidence: first=%+v second=%+v err=%v", first, second, err)
	}
	second, err = bootstrapGuestIdentityEvidence(binding, publicMaterialFixture(t), fence)
	if err != nil || first == second {
		t.Fatalf("client key did not alter evidence: first=%+v second=%+v err=%v", first, second, err)
	}
	second, err = bootstrapGuestIdentityEvidence(binding, key, "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff")
	if err != nil || first == second {
		t.Fatalf("isolation fence did not alter evidence: first=%+v second=%+v err=%v", first, second, err)
	}
	if _, err := bootstrapGuestIdentityEvidence(binding, key, "bad"); err == nil {
		t.Fatal("invalid fence digest accepted")
	}
}
