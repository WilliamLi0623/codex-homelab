package codexsession

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
)

func TestVisibleHistoryItemAllowsOnlyUserTextAndVisibleAssistantPhases(t *testing.T) {
	input := []json.RawMessage{
		json.RawMessage(`{"id":"u1","type":"userMessage","content":[{"type":"text","text":"hello"},{"type":"image","url":"https://example.invalid/image"}]}`),
		json.RawMessage(`{"id":"a1","type":"agentMessage","phase":"commentary","text":"working"}`),
		json.RawMessage(`{"id":"a2","type":"agentMessage","phase":"final_answer","text":"done"}`),
		json.RawMessage(`{"id":"a3","type":"agentMessage","text":"legacy visible text"}`),
		json.RawMessage(`{"id":"r1","type":"reasoning","text":"private reasoning"}`),
		json.RawMessage(`{"id":"t1","type":"functionCallOutput","output":"tool output"}`),
		json.RawMessage(`{"id":"a4","type":"agentMessage","phase":"analysis","text":"not a visible phase"}`),
	}

	var got []HistoryMessage
	for _, item := range input {
		message, ok := visibleHistoryItem(item)
		if ok {
			got = append(got, message)
		}
	}
	want := []HistoryMessage{
		{ID: "u1", Role: "user", Text: "hello"},
		{ID: "a1", Role: "assistant", Text: "working"},
		{ID: "a2", Role: "assistant", Text: "done"},
		{ID: "a3", Role: "assistant", Text: "legacy visible text"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("visible history = %#v, want %#v", got, want)
	}
}

func TestVisibleHistoryItemRejectsMalformedAndMissingIDs(t *testing.T) {
	for _, raw := range []json.RawMessage{
		json.RawMessage(`not json`),
		json.RawMessage(`{"type":"agentMessage","text":"missing id"}`),
		json.RawMessage(`{"id":"u2","type":"userMessage","content":[{"type":"image"}]}`),
	} {
		if message, ok := visibleHistoryItem(raw); ok {
			t.Errorf("visibleHistoryItem(%s) = %#v, true; want rejected", raw, message)
		}
	}
}

func TestFitHistoryMessageDoesNotReturnAnEmptyPartialRune(t *testing.T) {
	message := HistoryMessage{ID: "unicode", Role: "assistant", Text: "€"}
	got, ok := fitHistoryMessage(message, 1)
	if ok || got.Text != "" {
		t.Fatalf("fitHistoryMessage = (%+v, %v), want no message when a full rune cannot fit", got, ok)
	}
}

func TestSessionManagerHistoryLoadsRecentTurnsChronologically(t *testing.T) {
	client, serverConn := newFakeAppServerClient(t)
	manager := &SessionManager{client: client, sessions: map[string]*ManagedSession{"thread-1": {}}}
	serverDone := make(chan error, 1)
	go func() {
		initializeFakeAppServer(t, serverConn)
		turns := readAppServerRequest(t, serverConn)
		if turns.Method != "thread/turns/list" || turns.Params["threadId"] != "thread-1" || turns.Params["limit"] != float64(maxHistoryTurns) {
			serverDone <- errorsForTest("history did not request a bounded turn page")
			return
		}
		writeAppServerResponse(t, serverConn, turns.ID, map[string]any{
			"data": []any{map[string]any{"id": "turn-new"}, map[string]any{"id": "turn-old"}}, "nextCursor": "older-turns",
		})
		wrongItemDirection := false
		for _, turnID := range []string{"turn-new", "turn-old"} {
			items := readAppServerRequest(t, serverConn)
			if items.Method != "thread/items/list" || items.Params["turnId"] != turnID || items.Params["limit"] != float64(maxHistoryItemsTurn) {
				serverDone <- errorsForTest("history item request did not preserve turn and page bounds")
				writeAppServerResponse(t, serverConn, items.ID, map[string]any{"data": []any{}})
				return
			}
			if items.Params["sortDirection"] != "desc" {
				wrongItemDirection = true
			}
			data := []any{
				map[string]any{"id": "tool-" + turnID, "type": "functionCallOutput", "output": "must not be exposed"},
				map[string]any{"id": "assistant-" + turnID, "type": "agentMessage", "phase": "final_answer", "text": "answer " + turnID},
				map[string]any{"id": "reason-" + turnID, "type": "reasoning", "text": "must not be exposed"},
				map[string]any{"id": "user-" + turnID, "type": "userMessage", "content": []any{map[string]any{"type": "text", "text": "question " + turnID}}},
			}
			writeAppServerResponse(t, serverConn, items.ID, map[string]any{"data": data})
		}
		if wrongItemDirection {
			serverDone <- errorsForTest("history item request did not select the latest bounded items")
			return
		}
		serverDone <- nil
	}()

	history, err := manager.History(testAppServerContext(t), "thread-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
	want := []HistoryMessage{
		{ID: "user-turn-old", Role: "user", Text: "question turn-old"},
		{ID: "assistant-turn-old", Role: "assistant", Text: "answer turn-old"},
		{ID: "user-turn-new", Role: "user", Text: "question turn-new"},
		{ID: "assistant-turn-new", Role: "assistant", Text: "answer turn-new"},
	}
	if !reflect.DeepEqual(history.Messages, want) || !history.Truncated {
		t.Fatalf("history=%+v, want messages=%+v and truncated=true", history, want)
	}
}

func TestSessionManagerHistoryRejectsUnregisteredThread(t *testing.T) {
	client, _ := newFakeAppServerClient(t)
	manager := &SessionManager{client: client, sessions: map[string]*ManagedSession{}}
	if _, err := manager.History(context.Background(), "foreign-thread"); err == nil {
		t.Fatal("history for an unregistered thread succeeded")
	}
}
