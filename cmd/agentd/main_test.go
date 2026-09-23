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
	"github.com/WilliamLi0623/codex-homelab/internal/git"
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

func TestParseCodexVersion(t *testing.T) {
	for _, output := range []string{"codex-cli 0.156.1", "codex 0.155.0\n", "0.156.1"} {
		got, err := parseCodexVersion(output)
		if err != nil || got == "" {
			t.Fatalf("parseCodexVersion(%q) = %q, %v", output, got, err)
		}
	}
	for _, output := range []string{"", "Codex version latest", "0.156"} {
		if got, err := parseCodexVersion(output); err == nil || got != "" {
			t.Fatalf("parseCodexVersion(%q) = %q, %v; want error", output, got, err)
		}
	}
}

type commitRunner struct{ workspace string }

func (r commitRunner) Run(_ context.Context, _ string, name string, args ...string) (string, error) {
	if name == "git" && len(args) >= 2 && args[0] == "rev-parse" && args[1] == "--show-toplevel" {
		return r.workspace + "\n", nil
	}
	if name == "git" && len(args) >= 2 && args[0] == "rev-parse" && args[1] == "HEAD" {
		return "0123456789012345678901234567890123456789\n", nil
	}
	return "", nil
}

var _ git.Runner = commitRunner{}

func TestSessionRunCommitsConfiguredWorkspaceAfterTurn(t *testing.T) {
	workspacePath := filepath.Join(t.TempDir(), "attempt-1")
	environment := []string{
		"CODEX_WORKSPACE=" + workspacePath,
		`CODEX_VALIDATION_COMMAND=["go","test","./..."]`,
	}
	result, err := newSessionWithEnvironment(&fakeRunner{}, environment, commitRunner{workspace: workspacePath}).run(context.Background(), request{Prompt: "first"})
	if err != nil {
		t.Fatalf("session.run() error = %v", err)
	}
	if result.CommitSHA != "0123456789012345678901234567890123456789" {
		t.Fatalf("commit_sha = %q", result.CommitSHA)
	}
}

func TestToolLoopRejectsCompletionWithoutTerminalUse(t *testing.T) {
	workspacePath := filepath.Join(t.TempDir(), "attempt-1")
	environment := []string{
		"CODEX_MODEL=glm-5.3-flash",
		"CODEX_WORKSPACE=" + workspacePath,
		`CODEX_VALIDATION_COMMAND=["sh","-c","true"]`,
	}
	_, err := newSessionWithEnvironment(&fakeRunner{}, environment, commitRunner{workspace: workspacePath}).run(context.Background(), request{Prompt: "first"})
	if err == nil || !strings.Contains(err.Error(), "terminal tool call") {
		t.Fatalf("session.run() error = %v, want terminal tool call guard", err)
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
