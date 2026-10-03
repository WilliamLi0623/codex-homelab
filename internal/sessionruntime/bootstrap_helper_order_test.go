package sessionruntime

import (
	"testing"

	"github.com/WilliamLi0623/codex-homelab/internal/store"
)

func TestBootstrapHelperPrecedesArtifactVerification(t *testing.T) {
	stages := bootstrapStages()
	for i, stage := range stages {
		if stage == store.SessionBootstrapHelperVerified {
			if i == 0 || i+1 >= len(stages) || stages[i-1] != store.SessionBootstrapNetworkEnabled || stages[i+1] != store.SessionBootstrapArtifactVerified {
				t.Fatal("helper verification must follow network enablement and precede artifact installation")
			}
			return
		}
	}
	t.Fatal("bootstrap helper verification checkpoint is missing")
}
