package codexsession

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"
)

func TestSessionManagerPinsThreadAndFansOutEventsAndApprovals(t *testing.T) {
	client, serverConn := newFakeAppServerClient(t)
	manager, err := NewSessionManager(client)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)

	thread := AppServerThread{
		ID: "thread-1", ModelProvider: "openai", Model: "gpt-6-luna",
		ReasoningEffort: stringPointer("high"), ThreadSource: stringPointer(sessionUIServiceName),
	}
	pin := RoutePin{Mode: "normal", Provider: "openai", Model: "gpt-6-luna", Effort: "high", Generation: 7}
	session, err := manager.RegisterThread(thread, pin)
	if err != nil {
		t.Fatal(err)
	}
	if got := manager.Get("thread-1"); got == nil || got.Pin.Provider != pin.Provider || got.Pin.Model != pin.Model || got.Pin.Effort != pin.Effort || got.Pin.Generation != pin.Generation || got.Pin.CreatedAt.IsZero() {
		t.Fatalf("stored session pin=%+v", got)
	}

	if _, err := serverConn.Write([]byte(`{"method":"item/agentMessage/delta","params":{"threadId":"thread-1","delta":"hello"}}` + "\n")); err != nil {
		t.Fatal(err)
	}
	select {
	case notice := <-session.Notices:
		if notice.Event == nil || notice.Event.Method != "item/agentMessage/delta" {
			t.Fatalf("event notice=%+v", notice)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for session event")
	}

	if _, err := serverConn.Write([]byte(`{"id":"approval-1","method":"item/commandExecution/requestApproval","params":{"threadId":"thread-1","turnId":"turn-1","itemId":"item-1","command":"git status","cwd":"C:/repo","availableDecisions":["accept","decline","cancel","acceptForSession"]}}` + "\n")); err != nil {
		t.Fatal(err)
	}
	var approval ApprovalRequest
	select {
	case notice := <-session.Notices:
		if notice.Approval == nil {
			t.Fatalf("approval notice=%+v", notice)
		}
		approval = *notice.Approval
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for approval")
	}
	if approval.Command != "git status" || approval.CWD != "C:/repo" || len(approval.Decisions) != 3 {
		t.Fatalf("sanitized approval=%+v", approval)
	}

	responseLineCh := make(chan []byte, 1)
	responseErrCh := make(chan error, 1)
	go func() {
		_ = serverConn.SetReadDeadline(time.Now().Add(time.Second))
		line, err := bufio.NewReader(serverConn).ReadBytes('\n')
		if err != nil {
			responseErrCh <- err
			return
		}
		responseLineCh <- line
	}()
	if err := manager.RespondApproval(context.Background(), approval, "accept"); err != nil {
		t.Fatal(err)
	}
	var responseLine []byte
	select {
	case responseLine = <-responseLineCh:
	case err := <-responseErrCh:
		t.Fatal(err)
	case <-time.After(time.Second):
		t.Fatal("timed out reading approval response")
	}
	var response struct {
		ID     string            `json:"id"`
		Result map[string]string `json:"result"`
	}
	if err := json.Unmarshal(responseLine, &response); err != nil {
		t.Fatal(err)
	}
	if response.ID != "approval-1" || response.Result["decision"] != "accept" {
		t.Fatalf("approval response=%s", responseLine)
	}
	if err := manager.RespondApproval(context.Background(), approval, "accept"); err == nil {
		t.Fatal("a completed approval callback must never be submitted twice")
	}
}

func TestSessionManagerRejectsThreadWithoutSessionMarkerOrMismatchedRoute(t *testing.T) {
	client, _ := newFakeAppServerClient(t)
	manager, err := NewSessionManager(client)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)
	pin := RoutePin{Mode: "normal", Provider: "openai", Model: "gpt-6-luna", Effort: "high"}
	thread := AppServerThread{ID: "other", ModelProvider: "openai", Model: "gpt-6-luna", ReasoningEffort: stringPointer("high")}
	if _, err := manager.RegisterThread(thread, pin); err == nil {
		t.Fatal("thread without local UI marker must not be claimed")
	}
	thread.ThreadSource = stringPointer(sessionUIServiceName)
	thread.Model = "gpt-6-sol"
	if _, err := manager.RegisterThread(thread, pin); err == nil {
		t.Fatal("route mismatch must not be accepted")
	}
}

func TestSessionManagerExplicitlyRejectsUnsupportedPermissionApproval(t *testing.T) {
	client, serverConn := newFakeAppServerClient(t)
	manager, err := NewSessionManager(client)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)
	thread := AppServerThread{ID: "thread-1", ModelProvider: "openai", Model: "gpt-6-luna", ReasoningEffort: stringPointer("high"), ThreadSource: stringPointer(sessionUIServiceName)}
	session, err := manager.RegisterThread(thread, RoutePin{Mode: "normal", Provider: "openai", Model: "gpt-6-luna", Effort: "high"})
	if err != nil {
		t.Fatal(err)
	}
	responseLineCh := make(chan []byte, 1)
	go func() {
		line, _ := bufio.NewReader(serverConn).ReadBytes('\n')
		responseLineCh <- line
	}()
	if _, err := serverConn.Write([]byte(`{"id":91,"method":"item/permissions/requestApproval","params":{"threadId":"thread-1","turnId":"turn-1","itemId":"item-1","additionalPermissions":{"network":{"enabled":true}}}}` + "\n")); err != nil {
		t.Fatal(err)
	}
	select {
	case line := <-responseLineCh:
		var response struct {
			ID    int `json:"id"`
			Error struct {
				Code int `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal(line, &response); err != nil {
			t.Fatal(err)
		}
		if response.ID != 91 || response.Error.Code == 0 {
			t.Fatalf("unsupported approval response=%s", line)
		}
	case <-time.After(time.Second):
		t.Fatal("unsupported permission request was neither answered nor rejected")
	}
	select {
	case notice := <-session.Notices:
		if notice.Supported || notice.Error == "" || notice.Approval == nil {
			t.Fatalf("unsupported request notice=%+v", notice)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for unsupported approval notice")
	}
}

func TestSessionManagerCreateTurnApprovalResumeSecondTurnKeepsRoutePinned(t *testing.T) {
	client, serverConn := newFakeAppServerClient(t)
	manager, err := NewSessionManager(client)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)
	serverDone := make(chan error, 1)
	go func() {
		initializeFakeAppServer(t, serverConn)
		start := readAppServerRequest(t, serverConn)
		if start.Method != "thread/start" || start.Params["modelProvider"] != "openai" || start.Params["model"] != "gpt-6-luna" {
			serverDone <- errorsForTest("initial route was not pinned")
			return
		}
		thread := map[string]any{"id": "thread-1", "modelProvider": "openai", "model": "gpt-6-luna", "reasoningEffort": "high", "threadSource": sessionUIServiceName}
		writeAppServerResponse(t, serverConn, start.ID, map[string]any{"thread": thread, "modelProvider": "openai", "model": "gpt-6-luna", "reasoningEffort": "high", "approvalPolicy": "on-request"})
		firstTurn := readAppServerRequest(t, serverConn)
		if firstTurn.Method != "turn/start" || firstTurn.Params["threadId"] != "thread-1" {
			serverDone <- errorsForTest("first turn was not associated with the new thread")
			return
		}
		writeAppServerResponse(t, serverConn, firstTurn.ID, map[string]any{"turn": map[string]any{"id": "turn-1"}})
		if _, err := serverConn.Write([]byte(`{"id":8,"method":"item/commandExecution/requestApproval","params":{"threadId":"thread-1","turnId":"turn-1","itemId":"item-1","command":"go test ./...","availableDecisions":["accept","decline"]}}` + "\n")); err != nil {
			serverDone <- err
			return
		}
		approvalResponse := readApprovalWireResponse(t, serverConn)
		if approvalResponse.ID != 8 || approvalResponse.Result["decision"] != "accept" {
			serverDone <- errorsForTest("first turn approval was not returned")
			return
		}
		resume := readAppServerRequest(t, serverConn)
		if resume.Method != "thread/resume" || resume.Params["threadId"] != "thread-1" {
			serverDone <- errorsForTest("resume did not target the pinned thread")
			return
		}
		for _, key := range []string{"modelProvider", "model", "config"} {
			if _, exists := resume.Params[key]; exists {
				serverDone <- errorsForTest("resume attempted to change the provider route")
				return
			}
		}
		writeAppServerResponse(t, serverConn, resume.ID, map[string]any{
			"thread":        map[string]any{"id": "thread-1", "modelProvider": "openai", "model": "gpt-6-luna", "reasoningEffort": "high", "threadSource": sessionUIServiceName},
			"modelProvider": "openai", "model": "gpt-6-luna", "reasoningEffort": "high", "approvalPolicy": "on-request",
		})
		secondTurn := readAppServerRequest(t, serverConn)
		if secondTurn.Method != "turn/start" || secondTurn.Params["threadId"] != "thread-1" {
			serverDone <- errorsForTest("second turn changed threads")
			return
		}
		writeAppServerResponse(t, serverConn, secondTurn.ID, map[string]any{"turn": map[string]any{"id": "turn-2"}})
		interrupt := readAppServerRequest(t, serverConn)
		if interrupt.Method != "turn/interrupt" || interrupt.Params["threadId"] != "thread-1" || interrupt.Params["turnId"] != "turn-2" {
			serverDone <- errorsForTest("interrupt did not target the active turn")
			return
		}
		writeAppServerResponse(t, serverConn, interrupt.ID, map[string]any{})
		serverDone <- nil
	}()

	ctx := testAppServerContext(t)
	pin := RoutePin{Mode: "normal", Provider: "openai", Model: "gpt-6-luna", Effort: "high", Generation: 4}
	session, err := manager.CreateThread(ctx, ThreadStartOptions{CWD: "C:/repo", ModelProvider: pin.Provider, Model: pin.Model, Effort: pin.Effort}, pin)
	if err != nil {
		t.Fatal(err)
	}
	firstTurn, err := manager.StartTurn(ctx, session.Thread.ID, "run a test")
	if err != nil || firstTurn.ID != "turn-1" {
		t.Fatalf("first turn=%+v err=%v", firstTurn, err)
	}
	var approval ApprovalRequest
	select {
	case notice := <-session.Notices:
		if notice.Approval == nil || !notice.Supported {
			t.Fatalf("approval notice=%+v", notice)
		}
		approval = *notice.Approval
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for first-turn approval")
	}
	if err := manager.RespondApproval(ctx, approval, "accept"); err != nil {
		t.Fatal(err)
	}
	resumed, err := manager.ResumeThread(ctx, session.Thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Pin != session.Pin {
		t.Fatalf("resume changed route pin: before=%+v after=%+v", session.Pin, resumed.Pin)
	}
	secondTurn, err := manager.StartTurn(ctx, resumed.Thread.ID, "continue")
	if err != nil || secondTurn.ID != "turn-2" {
		t.Fatalf("second turn=%+v err=%v", secondTurn, err)
	}
	if err := manager.InterruptTurn(ctx, resumed.Thread.ID, secondTurn.ID); err != nil {
		t.Fatal(err)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}

func TestSessionManagerRecoversRoutePinsFromMarkedAppServerHistory(t *testing.T) {
	client, serverConn := newFakeAppServerClient(t)
	manager, err := NewSessionManager(client)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)
	go func() {
		initializeFakeAppServer(t, serverConn)
		request := readAppServerRequest(t, serverConn)
		if request.Method != "thread/list" {
			return
		}
		writeAppServerResponse(t, serverConn, request.ID, map[string]any{"data": []any{
			map[string]any{"id": "fallback-thread", "modelProvider": "osc", "model": "muse-spark-1.3-contributor", "reasoningEffort": "xhigh", "threadSource": sessionUIServiceName, "createdAt": int64(1790000000)},
			map[string]any{"id": "unrelated", "modelProvider": "openai", "model": "gpt-6-luna", "reasoningEffort": "high", "threadSource": "interactive"},
		}, "nextCursor": ""})
	}()
	sessions, err := manager.RecoverListedThreads(testAppServerContext(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].Thread.ID != "fallback-thread" || sessions[0].Pin.Mode != "quota_fallback" || sessions[0].Pin.Generation != 0 {
		t.Fatalf("recovered sessions=%+v", sessions)
	}
}

func TestRoutePinFromThreadRejectsUnrecognizedProviderTuple(t *testing.T) {
	thread := AppServerThread{ID: "unknown", ModelProvider: "custom", Model: "arbitrary", ReasoningEffort: stringPointer("high"), ThreadSource: stringPointer(sessionUIServiceName)}
	if _, err := RoutePinFromThread(thread); err == nil {
		t.Fatal("unknown provider/model/effort tuple must not be guessed after restart")
	}
}

func readApprovalWireResponse(t *testing.T, conn net.Conn) struct {
	ID     int               `json:"id"`
	Result map[string]string `json:"result"`
} {
	t.Helper()
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		ID     int               `json:"id"`
		Result map[string]string `json:"result"`
	}
	if err := json.Unmarshal(line, &response); err != nil {
		t.Fatal(err)
	}
	return response
}

func stringPointer(value string) *string { return &value }
