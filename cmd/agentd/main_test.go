package main

import (
	"strings"
	"testing"

	"github.com/WilliamLi0623/codex-homelab/internal/agentd"
)

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
