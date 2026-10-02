package codexsession

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

func newFakeAppServerClient(t *testing.T) (*AppServerClient, net.Conn) {
	t.Helper()
	clientConn, serverConn := net.Pipe()
	client := NewAppServerClient(NewProtocolClient(clientConn, clientConn))
	if err := client.protocol.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = client.protocol.Close()
		_ = serverConn.Close()
	})
	return client, serverConn
}

type appServerRequest struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params map[string]any  `json:"params"`
}

func readAppServerRequest(t *testing.T, conn net.Conn) appServerRequest {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var request appServerRequest
	if err := json.Unmarshal(line, &request); err != nil {
		t.Fatal(err)
	}
	return request
}

func writeAppServerResponse(t *testing.T, conn net.Conn, id json.RawMessage, result any) {
	t.Helper()
	line, err := json.Marshal(map[string]any{"id": id, "result": result})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write(append(line, '\n')); err != nil {
		t.Fatal(err)
	}
}

func testAppServerContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	t.Cleanup(cancel)
	return ctx
}

func initializeFakeAppServer(t *testing.T, conn net.Conn) {
	t.Helper()
	initialize := readAppServerRequest(t, conn)
	if initialize.Method != "initialize" {
		t.Fatalf("first RPC=%q, want initialize", initialize.Method)
	}
	writeAppServerResponse(t, conn, initialize.ID, map[string]any{})
	initialized := readAppServerRequest(t, conn)
	if initialized.Method != "initialized" {
		t.Fatalf("initialization notification=%q, want initialized", initialized.Method)
	}
}

func TestAppServerClientInitializesAndStartsRoutePinnedThread(t *testing.T) {
	client, serverConn := newFakeAppServerClient(t)
	serverDone := make(chan error, 1)
	go func() {
		initialize := readAppServerRequest(t, serverConn)
		if initialize.Method != "initialize" {
			serverDone <- unexpectedMethodError(initialize.Method)
			return
		}
		writeAppServerResponse(t, serverConn, initialize.ID, map[string]any{})
		initialized := readAppServerRequest(t, serverConn)
		if initialized.Method != "initialized" {
			serverDone <- unexpectedMethodError(initialized.Method)
			return
		}
		start := readAppServerRequest(t, serverConn)
		if start.Method != "thread/start" {
			serverDone <- unexpectedMethodError(start.Method)
			return
		}
		if start.Params["modelProvider"] != "cch_bridge" || start.Params["model"] != "glm-5.3-flash" || start.Params["ephemeral"] != false || start.Params["threadSource"] != sessionUIServiceName {
			serverDone <- errorsForTest("thread/start omitted the pinned route or persistence mode")
			return
		}
		config, ok := start.Params["config"].(map[string]any)
		if !ok || config["model_reasoning_effort"] != "max" {
			serverDone <- errorsForTest("thread/start omitted session-scoped reasoning effort")
			return
		}
		if _, exists := start.Params["approvalPolicy"]; exists {
			serverDone <- errorsForTest("thread/start overrode the user's approval policy")
			return
		}
		if _, exists := start.Params["sandbox"]; exists {
			serverDone <- errorsForTest("thread/start overrode the user's sandbox")
			return
		}
		writeAppServerResponse(t, serverConn, start.ID, map[string]any{
			"modelProvider": "cch_bridge", "model": "glm-5.3-flash", "reasoningEffort": "max", "approvalPolicy": "on-request",
			"thread": map[string]any{"id": "thread-1", "modelProvider": "cch_bridge", "model": "glm-5.3-flash", "reasoningEffort": "max", "ephemeral": false, "cwd": "C:/repo"},
		})
		serverDone <- nil
	}()

	ctx := testAppServerContext(t)
	if err := client.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	thread, err := client.StartThread(ctx, ThreadStartOptions{
		CWD: "C:/repo", ModelProvider: "cch_bridge", Model: "glm-5.3-flash", Effort: "max",
		Config: map[string]any{"model_provider": "cch_bridge", "model_reasoning_effort": "max"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if thread.ID != "thread-1" || thread.ModelProvider != "cch_bridge" || thread.Model != "glm-5.3-flash" || thread.ReasoningEffort == nil || *thread.ReasoningEffort != "max" || thread.ApprovalPolicy != "on-request" {
		t.Fatalf("thread route metadata=%+v", thread)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}

func TestAppServerClientReadsSanitizedAccountAndMatchesEmailWithoutReturningIt(t *testing.T) {
	client, serverConn := newFakeAppServerClient(t)
	serverDone := make(chan error, 1)
	go func() {
		initializeFakeAppServer(t, serverConn)
		request := readAppServerRequest(t, serverConn)
		if request.Method != "account/read" || request.Params["refreshToken"] != false {
			serverDone <- errorsForTest("account/read did not request a non-refreshing account snapshot")
			return
		}
		writeAppServerResponse(t, serverConn, request.ID, map[string]any{
			"account": map[string]any{
				"email": "codex-a@example.invalid", "planType": "plus", "type": "chatgpt",
			},
			"requiresOpenaiAuth": false,
		})
		serverDone <- nil
	}()

	account, err := client.ReadAccount(testAppServerContext(t))
	if err != nil {
		t.Fatal(err)
	}
	if account.Type != "chatgpt" || account.PlanType != "plus" || account.RequiresOpenAIAuth {
		t.Fatalf("account summary=%+v", account)
	}
	if strings.Contains(fmt.Sprintf("%+v %#v", account, account), "codex-a@example.invalid") {
		t.Fatal("sanitized account summary exposed the raw email")
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}

func TestAppServerClientMatchesAccountEmailWithoutReturningIt(t *testing.T) {
	client, serverConn := newFakeAppServerClient(t)
	serverDone := make(chan error, 1)
	go func() {
		initializeFakeAppServer(t, serverConn)
		request := readAppServerRequest(t, serverConn)
		if request.Method != "account/read" || request.Params["refreshToken"] != false {
			serverDone <- errorsForTest("account email comparison did not use a non-refreshing account/read")
			return
		}
		writeAppServerResponse(t, serverConn, request.ID, map[string]any{
			"account": map[string]any{
				"email": "codex-a@example.invalid", "planType": "plus", "type": "chatgpt",
			},
			"requiresOpenaiAuth": false,
		})
		serverDone <- nil
	}()

	matched, err := client.AccountEmailMatches(testAppServerContext(t), "codex-a@example.invalid")
	if err != nil || !matched {
		t.Fatalf("AccountEmailMatches()=(%v, %v), want (true, nil)", matched, err)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}

func TestAppServerClientDoesNotMatchDifferentAccountEmail(t *testing.T) {
	client, serverConn := newFakeAppServerClient(t)
	serverDone := make(chan error, 1)
	go func() {
		initializeFakeAppServer(t, serverConn)
		request := readAppServerRequest(t, serverConn)
		writeAppServerResponse(t, serverConn, request.ID, map[string]any{
			"account": map[string]any{
				"email": "codex-a@example.invalid", "planType": "plus", "type": "chatgpt",
			},
			"requiresOpenaiAuth": false,
		})
		serverDone <- nil
	}()

	matched, err := client.AccountEmailMatches(testAppServerContext(t), "codex-b@example.invalid")
	if err != nil || matched {
		t.Fatalf("AccountEmailMatches()=(%v, %v), want (false, nil)", matched, err)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}

func TestAppServerClientRejectsEmptyExpectedAccountEmailBeforeRPC(t *testing.T) {
	client, _ := newFakeAppServerClient(t)
	matched, err := client.AccountEmailMatches(testAppServerContext(t), " \t ")
	if err == nil || matched {
		t.Fatalf("AccountEmailMatches()=(%v, %v), want (false, error)", matched, err)
	}
}

func TestAppServerClientRejectsIncompleteAccountReadWithoutLeakingResponse(t *testing.T) {
	client, serverConn := newFakeAppServerClient(t)
	serverDone := make(chan error, 1)
	go func() {
		initializeFakeAppServer(t, serverConn)
		request := readAppServerRequest(t, serverConn)
		writeAppServerResponse(t, serverConn, request.ID, map[string]any{
			"account":            map[string]any{"email": "sensitive@example.invalid", "planType": "plus"},
			"requiresOpenaiAuth": false,
		})
		serverDone <- nil
	}()

	_, err := client.ReadAccount(testAppServerContext(t))
	if err == nil || strings.Contains(err.Error(), "sensitive@example.invalid") {
		t.Fatalf("ReadAccount() error=%v; want sanitized incomplete-account error", err)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}

func TestAppServerClientResumeDoesNotOverridePinnedProvider(t *testing.T) {
	client, serverConn := newFakeAppServerClient(t)
	go func() {
		initializeFakeAppServer(t, serverConn)
		request := readAppServerRequest(t, serverConn)
		if request.Method != "thread/resume" || request.Params["threadId"] != "thread-1" {
			return
		}
		for _, key := range []string{"modelProvider", "model", "config", "approvalPolicy", "sandbox"} {
			if _, exists := request.Params[key]; exists {
				return
			}
		}
		writeAppServerResponse(t, serverConn, request.ID, map[string]any{
			"modelProvider": "osc", "model": "muse-spark-1.3-contributor", "reasoningEffort": "xhigh", "approvalPolicy": "on-request",
			"thread": map[string]any{"id": "thread-1", "modelProvider": "osc", "model": "muse-spark-1.3-contributor", "reasoningEffort": "xhigh", "ephemeral": false},
		})
	}()
	thread, err := client.ResumeThread(testAppServerContext(t), "thread-1")
	if err != nil {
		t.Fatal(err)
	}
	if thread.ModelProvider != "osc" || thread.Model != "muse-spark-1.3-contributor" || thread.ReasoningEffort == nil || *thread.ReasoningEffort != "xhigh" {
		t.Fatalf("resumed route changed: %+v", thread)
	}
}

func TestAppServerClientTurnStartPreservesApprovalAndSandboxDefaults(t *testing.T) {
	client, serverConn := newFakeAppServerClient(t)
	go func() {
		initializeFakeAppServer(t, serverConn)
		request := readAppServerRequest(t, serverConn)
		if request.Method != "turn/start" || request.Params["threadId"] != "thread-1" {
			return
		}
		for _, key := range []string{"approvalPolicy", "sandboxPolicy", "effort", "model"} {
			if _, exists := request.Params[key]; exists {
				return
			}
		}
		input, ok := request.Params["input"].([]any)
		if !ok || len(input) != 1 {
			return
		}
		item, ok := input[0].(map[string]any)
		if !ok || item["type"] != "text" || item["text"] != "hello" {
			return
		}
		writeAppServerResponse(t, serverConn, request.ID, map[string]any{"turn": map[string]any{"id": "turn-1"}})
	}()
	turn, err := client.StartTurn(testAppServerContext(t), "thread-1", "hello")
	if err != nil {
		t.Fatal(err)
	}
	if turn.ID != "turn-1" {
		t.Fatalf("turn id=%q", turn.ID)
	}
}

func TestAppServerClientListsThreadsAndPreservesPaginationCursor(t *testing.T) {
	client, serverConn := newFakeAppServerClient(t)
	go func() {
		initializeFakeAppServer(t, serverConn)
		request := readAppServerRequest(t, serverConn)
		if request.Method != "thread/list" || request.Params["limit"] != float64(25) || request.Params["cursor"] != "next-1" {
			return
		}
		sourceKinds, ok := request.Params["sourceKinds"].([]any)
		if !ok || len(sourceKinds) != 1 || sourceKinds[0] != "appServer" {
			return
		}
		writeAppServerResponse(t, serverConn, request.ID, map[string]any{
			"data":       []any{map[string]any{"id": "thread-1", "modelProvider": "openai", "model": "gpt-6-luna", "reasoningEffort": "high"}},
			"nextCursor": "next-2",
		})
	}()
	page, err := client.ListThreads(testAppServerContext(t), 25, "next-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Threads) != 1 || page.Threads[0].ID != "thread-1" || page.NextCursor != "next-2" {
		t.Fatalf("thread page=%+v", page)
	}
}

type testError string

func (e testError) Error() string { return string(e) }

func errorsForTest(message string) error { return testError(message) }
