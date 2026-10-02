package main

import "testing"

func TestParseArgsUsesOnlyGenerationDigestAsRemoteSelector(t *testing.T) {
	digest := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	tests := []struct {
		name        string
		args        []string
		wantObserve bool
		wantDigest  string
		wantOK      bool
	}{
		{name: "install", args: []string{"codex-artifact-install", digest}, wantDigest: digest, wantOK: true},
		{name: "observe", args: []string{"codex-artifact-install", "--observe", digest}, wantObserve: true, wantDigest: digest, wantOK: true},
		{name: "version selector rejected", args: []string{"codex-artifact-install", "0.160.0", digest}},
		{name: "versioned observe rejected", args: []string{"codex-artifact-install", "--observe", "0.160.0", digest}},
		{name: "unknown mode rejected", args: []string{"codex-artifact-install", "--delete", digest}},
		{name: "missing digest rejected", args: []string{"codex-artifact-install"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			observe, gotDigest, ok := parseArgs(test.args)
			if observe != test.wantObserve || gotDigest != test.wantDigest || ok != test.wantOK {
				t.Fatalf("parseArgs(%q) = (%t, %q, %t), want (%t, %q, %t)", test.args, observe, gotDigest, ok, test.wantObserve, test.wantDigest, test.wantOK)
			}
		})
	}
}
