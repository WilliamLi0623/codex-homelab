package capacity

import (
	"errors"
	"sort"
	"strings"
)

var ErrMetadataInvalid = errors.New("worker metadata is invalid")

const ManagedBy = "codex-homelab"

// WorkerMetadata returns the ownership metadata required on every dynamic
// worker. The map is copied by callers before handing it to an external API.
func WorkerMetadata(generation, taskID, created string) (map[string]string, error) {
	if strings.TrimSpace(generation) == "" || strings.TrimSpace(taskID) == "" || strings.TrimSpace(created) == "" {
		return nil, ErrMetadataInvalid
	}
	return map[string]string{
		"managed-by":      ManagedBy,
		"generation":      generation,
		"task":            taskID,
		"execution-class": "dedicated-lxc",
		"created":         created,
	}, nil
}

// EncodeMetadata is deterministic so request bodies and identity evidence can
// be compared across retries without relying on map iteration order.
func EncodeMetadata(metadata map[string]string) (string, error) {
	if len(metadata) == 0 {
		return "", ErrMetadataInvalid
	}
	keys := make([]string, 0, len(metadata))
	for key, value := range metadata {
		if strings.TrimSpace(key) == "" || strings.ContainsAny(key, "\r\n=;") || strings.TrimSpace(value) == "" || strings.ContainsAny(value, "\r\n") {
			return "", ErrMetadataInvalid
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+metadata[key])
	}
	return strings.Join(parts, "\n"), nil
}
