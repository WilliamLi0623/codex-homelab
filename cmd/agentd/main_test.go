package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/WilliamLi0623/codex-homelab/internal/agentd"
)

func TestSessionRunJSONLCommitSHAIsIncludedOnlyWhenValid(t *testing.T) {
	const validSHA = "0123456789abcdef0123456789abcdef01234567"
	for _, tc := range []struct {
		name    string
		content string
		create  bool
		wantSHA string
	}{
		{name: "valid", content: validSHA, create: true, wantSHA: validSHA},
		{name: "missing", wantSHA: ""},
		{name: "invalid", content: "not-a-commit", create: true, wantSHA: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			commitSHAFile := filepath.Join(t.TempDir(), "commit-sha")
			if tc.create {
				if err := os.WriteFile(commitSHAFile, []byte(tc.content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("CODEX_COMMIT_SHA_FILE", commitSHAFile)

			result, err := newSession(&fakeRunner{}).run(context.Background(), request{Prompt: "first"})
			if err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			if err := json.NewEncoder(&output).Encode(result); err != nil {
				t.Fatal(err)
			}
			var got response
			if err := json.NewDecoder(&output).Decode(&got); err != nil {
				t.Fatal(err)
			}
			if got.CommitSHA != tc.wantSHA {
				t.Fatalf("commit_sha=%q, want %q; JSONL=%s", got.CommitSHA, tc.wantSHA, output.String())
			}
			if tc.wantSHA == "" && strings.Contains(output.String(), "commit_sha") {
				t.Fatalf("JSONL result unexpectedly contains commit_sha: %s", output.String())
			}
		})
	}
}

func TestDecodeRequestRequiresPrompt(t *testing.T) {
	if _, err := decodeRequest(strings.NewReader(`{"prompt":""}`)); err == nil {
		t.Fatal("empty prompt accepted")
	}
}
func TestSummarizeEventsOmitsEventPayloads(t *testing.T) {
	events := []agentd.Event{{Method: "turn/started", Params: []byte(`{"secret":"do-not-print"}`)}, {Method: "turn/completed", Params: []byte(`{"output":"hidden"}`)}}
	got := summarizeEvents(events)
	if len(got) != 2 || got[0] != "turn/started" || got[1] != "turn/completed" || strings.Contains(strings.Join(got, " "), "secret") {
		t.Fatalf("summary=%v", got)
	}
}
