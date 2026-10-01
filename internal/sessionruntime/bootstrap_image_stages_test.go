package sessionruntime

import (
	"context"
	"io"
	"runtime"
	"strings"
	"testing"

	"github.com/WilliamLi0623/codex-homelab/internal/store"
)

func TestBootstrapImageBackupStageAppliesOnceAndObservesWithoutReplay(t *testing.T) {
	backups, binding := backupStoreFixture(t)
	roots, ok := bootstrapArchivePolicyRoots(bootstrapArchivePolicySessionImageCredentialsV2)
	if !ok {
		t.Fatal("Session image archive policy missing")
	}
	archive := bootstrapArchiveFromRoots(t, roots)
	var calls int
	stage := &bootstrapImageBackupStage{backups: backups, export: func(_ context.Context, got store.SessionRuntimeBinding, dst io.Writer) (bootstrapTLSArchiveEvidence, error) {
		calls++
		if got != binding {
			t.Fatal("export binding changed")
		}
		_, err := dst.Write(archive)
		return bootstrapTLSArchiveEvidence{Bytes: int64(len(archive)), SHA256: materialDigest(archive), IsolationSHA256: strings.Repeat("a", 64)}, err
	}}
	evidence, err := stage.Apply(context.Background(), binding)
	if err != nil || !validBootstrapEvidence(evidence) || calls != 1 {
		t.Fatalf("Apply() evidence=%+v calls=%d err=%v", evidence, calls, err)
	}
	observed, verified, err := stage.Observe(context.Background(), binding)
	if err != nil || !verified || observed != evidence || calls != 1 {
		t.Fatalf("Observe() evidence=%+v verified=%t calls=%d err=%v", observed, verified, calls, err)
	}
	if _, err := stage.Apply(context.Background(), binding); err == nil || calls != 1 {
		t.Fatalf("duplicate Apply() err=%v calls=%d, want refusal without re-export", err, calls)
	}
}

func TestBootstrapImageSanitizedStageRequiresExactBackupBeforeMutation(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("private backup store requires Linux ownership checks")
	}
	backups, binding := backupStoreFixture(t)
	mutations := 0
	result := bootstrapImageSanitationResult{UID: 0, OS: "Linux", Hostname: sessionHostname(binding.SessionID, binding.EpochID, binding.Generation), Policy: "session-image-credentials-v2", IsolationSHA256: strings.Repeat("b", 64)}
	stage := &bootstrapImageSanitizedStage{backups: backups, apply: func(context.Context, store.SessionRuntimeBinding) (bootstrapImageSanitationResult, error) {
		mutations++
		return result, nil
	}, observe: func(context.Context, store.SessionRuntimeBinding) (bootstrapImageSanitationResult, error) {
		return result, nil
	}}
	if _, err := stage.Apply(context.Background(), binding); err == nil || mutations != 0 {
		t.Fatalf("Apply without backup err=%v mutations=%d, want fail before guest action", err, mutations)
	}
	if _, _, err := stage.Observe(context.Background(), binding); err == nil || mutations != 0 {
		t.Fatalf("Observe without backup err=%v mutations=%d, want fail without guest action", err, mutations)
	}

	roots, ok := bootstrapArchivePolicyRoots(bootstrapArchivePolicySessionImageCredentialsV2)
	if !ok {
		t.Fatal("Session image archive policy missing")
	}
	archive := bootstrapArchiveFromRoots(t, roots)
	if _, err := backups.save(context.Background(), binding, func(_ context.Context, _ store.SessionRuntimeBinding, dst io.Writer) (bootstrapTLSArchiveEvidence, error) {
		_, err := dst.Write(archive)
		return bootstrapTLSArchiveEvidence{Bytes: int64(len(archive)), SHA256: materialDigest(archive), IsolationSHA256: strings.Repeat("c", 64)}, err
	}); err != nil {
		t.Fatalf("save exact policy fixture: %v", err)
	}
	evidence, err := stage.Apply(context.Background(), binding)
	if err != nil || !validBootstrapEvidence(evidence) || mutations != 1 {
		t.Fatalf("Apply with exact backup evidence=%+v mutations=%d err=%v", evidence, mutations, err)
	}
	observed, verified, err := stage.Observe(context.Background(), binding)
	if err != nil || !verified || observed != evidence || mutations != 1 {
		t.Fatalf("Observe() evidence=%+v verified=%t mutations=%d err=%v", observed, verified, mutations, err)
	}
}
