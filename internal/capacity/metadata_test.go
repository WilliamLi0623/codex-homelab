package capacity

import (
	"errors"
	"testing"
)

func TestWorkerMetadataAndEncodingAreDeterministic(t *testing.T) {
	metadata, err := WorkerMetadata("gen-1", "task-1", "2026-09-20T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := EncodeMetadata(metadata)
	if err != nil {
		t.Fatal(err)
	}
	want := "created=2026-09-20T00:00:00Z\nexecution-class=dedicated-lxc\ngeneration=gen-1\nmanaged-by=codex-homelab\ntask=task-1"
	if encoded != want {
		t.Fatalf("metadata = %q, want %q", encoded, want)
	}
}

func TestEncodeMetadataRejectsUnsafeValues(t *testing.T) {
	for _, metadata := range []map[string]string{{"": "value"}, {"key": ""}, {"key=value": "value"}, {"key": "line\nbreak"}} {
		if _, err := EncodeMetadata(metadata); !errors.Is(err, ErrMetadataInvalid) {
			t.Fatalf("metadata %+v error = %v", metadata, err)
		}
	}
}
