package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestBootstrapCompletionRejectsMissingHelperPredecessor(t *testing.T) {
	for _, status := range []SessionBootstrapStatus{SessionBootstrapIntent, SessionBootstrapUnknown} {
		t.Run(string(status), func(t *testing.T) {
			ctx := context.Background()
			s := newBootstrapTestStore(t, filepath.Join(t.TempDir(), "state.sqlite"), "bootstrap-generation-1")
			// Reproduce an incomplete historical artifact row retained by migration.
			_, err := s.db.ExecContext(ctx, `INSERT INTO session_bootstrap_checkpoints(runtime_binding_id,generation,stage,status) VALUES (?,?,?,?)`, "binding-1", "bootstrap-generation-1", SessionBootstrapArtifactVerified, status)
			if err != nil {
				t.Fatal(err)
			}
			err = s.CompleteSessionBootstrapStage(ctx, "binding-1", "bootstrap-generation-1", SessionBootstrapArtifactVerified, status, SessionBootstrapEvidence{SHA256: bootstrapTestDigest})
			if !errors.Is(err, ErrSessionBootstrapConflict) {
				t.Fatalf("completion without helper = %v", err)
			}
			row, err := s.GetSessionBootstrapStage(ctx, "binding-1", "bootstrap-generation-1", SessionBootstrapArtifactVerified)
			if err != nil || row.Status != status || row.Evidence.SHA256 != "" {
				t.Fatalf("rejected completion changed evidence: %+v, %v", row, err)
			}
		})
	}
}
