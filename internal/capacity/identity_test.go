package capacity

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestDynamicHostnameIsDeterministicAndBounded(t *testing.T) {
	first, err := DynamicHostname(3017, "sha256:ABCDEF0123456789-long-generation")
	if err != nil {
		t.Fatal(err)
	}
	second, err := DynamicHostname(3017, "sha256:ABCDEF0123456789-long-generation")
	if err != nil || first != second || len(first) > 63 || !strings.HasPrefix(first, "codex-lxc-3017-") {
		t.Fatalf("hostname = %q, second = %q, err = %v", first, second, err)
	}
}

func TestDynamicHostnameRejectsProtectedOrMissingIdentity(t *testing.T) {
	for _, input := range [][2]string{{"220", "gen"}, {"3010", ""}, {"3010", "!!!"}} {
		var vmid int
		_, _ = fmt.Sscanf(input[0], "%d", &vmid)
		if _, err := DynamicHostname(vmid, input[1]); !errors.Is(err, ErrIdentityInvalid) {
			t.Fatalf("DynamicHostname(%v) error = %v", input, err)
		}
	}
}
