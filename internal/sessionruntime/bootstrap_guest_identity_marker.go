package sessionruntime

import (
	"bytes"
	"encoding/json"
	"errors"

	"github.com/WilliamLi0623/codex-homelab/internal/store"
)

// This marker binds installation intent only; it is not proof of guest readiness.
func bootstrapGuestIdentityMarkerData(binding store.SessionRuntimeBinding, key string) ([]byte, error) {
	if !validMaterialBinding(binding) || !validMaterialPublicKey(key) {
		return nil, errors.New("invalid guest identity marker input")
	}
	return json.Marshal(struct {
		Version         int                         `json:"version"`
		Binding         store.SessionRuntimeBinding `json:"binding"`
		ClientPublicKey string                      `json:"client_public_key"`
	}{1, materialBinding(binding), key})
}

func bootstrapGuestIdentityMarkerMatches(data []byte, binding store.SessionRuntimeBinding, key string) bool {
	if len(data) == 0 || len(data) > 4096 {
		return false
	}
	expected, err := bootstrapGuestIdentityMarkerData(binding, key)
	return err == nil && bytes.Equal(bytes.TrimSpace(data), expected)
}
