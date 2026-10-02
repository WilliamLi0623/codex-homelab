package codexsession

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestStartDeviceCodeLoginUsesCodexDeviceFlowAndReturnsEphemeralChallenge(t *testing.T) {
	client, serverConn := newFakeAppServerClient(t)
	serverDone := make(chan error, 1)
	go func() {
		initializeFakeAppServer(t, serverConn)
		request := readAppServerRequest(t, serverConn)
		if request.Method != "account/login/start" {
			serverDone <- unexpectedMethodError(request.Method)
			return
		}
		if request.Params["type"] != "chatgptDeviceCode" || len(request.Params) != 1 {
			serverDone <- errorsForTest("device login requested with non-device parameters")
			return
		}
		writeAppServerResponse(t, serverConn, request.ID, map[string]string{
			"type":            "chatgptDeviceCode",
			"loginId":         "b8675a78-9052-4f83-b76a-8ae4f5f44821",
			"verificationUrl": "https://auth.openai.com/codex/device",
			"userCode":        "ABCD-EFGH",
		})
		serverDone <- nil
	}()

	challenge, err := client.StartDeviceCodeLogin(testAppServerContext(t))
	if err != nil {
		t.Fatalf("StartDeviceCodeLogin() error = %v", err)
	}
	if challenge.LoginID != "b8675a78-9052-4f83-b76a-8ae4f5f44821" || challenge.VerificationURL != "https://auth.openai.com/codex/device" || challenge.UserCode != "ABCD-EFGH" {
		t.Fatalf("StartDeviceCodeLogin() returned unexpected challenge: %+v", challenge)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}

func TestStartDeviceCodeLoginRejectsUnexpectedVerificationURL(t *testing.T) {
	client, serverConn := newFakeAppServerClient(t)
	serverDone := make(chan error, 1)
	go func() {
		initializeFakeAppServer(t, serverConn)
		request := readAppServerRequest(t, serverConn)
		writeAppServerResponse(t, serverConn, request.ID, map[string]string{
			"type":            "chatgptDeviceCode",
			"loginId":         "b8675a78-9052-4f83-b76a-8ae4f5f44821",
			"verificationUrl": "https://attacker.invalid/codex/device",
			"userCode":        "ABCD-EFGH",
		})
		cancel := readAppServerRequest(t, serverConn)
		if cancel.Method != "account/login/cancel" || cancel.Params["loginId"] != "b8675a78-9052-4f83-b76a-8ae4f5f44821" {
			serverDone <- errorsForTest("invalid device challenge was not cancelled by its login ID")
			return
		}
		writeAppServerResponse(t, serverConn, cancel.ID, map[string]string{"status": "canceled"})
		serverDone <- nil
	}()

	if _, err := client.StartDeviceCodeLogin(testAppServerContext(t)); err == nil {
		t.Fatal("StartDeviceCodeLogin() accepted a non-OpenAI verification URL")
	}
	select {
	case err := <-serverDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("device login cancellation was not sent after invalid challenge")
	}
}

func TestCancelDeviceCodeLoginReportsWhetherAttemptWasCancelled(t *testing.T) {
	client, serverConn := newFakeAppServerClient(t)
	serverDone := make(chan error, 1)
	go func() {
		initializeFakeAppServer(t, serverConn)
		request := readAppServerRequest(t, serverConn)
		if request.Method != "account/login/cancel" || request.Params["loginId"] != "b8675a78-9052-4f83-b76a-8ae4f5f44821" {
			serverDone <- errorsForTest("device login cancellation omitted login ID")
			return
		}
		writeAppServerResponse(t, serverConn, request.ID, map[string]string{"status": "canceled"})
		serverDone <- nil
	}()

	canceled, err := client.CancelDeviceCodeLogin(testAppServerContext(t), "b8675a78-9052-4f83-b76a-8ae4f5f44821")
	if err != nil || !canceled {
		t.Fatalf("CancelDeviceCodeLogin() = (%v, %v), want (true, nil)", canceled, err)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}

func TestStartDeviceCodeLoginRejectsMarkupInOneTimeCode(t *testing.T) {
	client, serverConn := newFakeAppServerClient(t)
	serverDone := make(chan error, 1)
	go func() {
		initializeFakeAppServer(t, serverConn)
		request := readAppServerRequest(t, serverConn)
		writeAppServerResponse(t, serverConn, request.ID, map[string]string{
			"type":            "chatgptDeviceCode",
			"loginId":         "b8675a78-9052-4f83-b76a-8ae4f5f44821",
			"verificationUrl": "https://auth.openai.com/codex/device",
			"userCode":        "<script>alert(1)</script>",
		})
		cancel := readAppServerRequest(t, serverConn)
		if cancel.Method != "account/login/cancel" || cancel.Params["loginId"] != "b8675a78-9052-4f83-b76a-8ae4f5f44821" {
			serverDone <- errorsForTest("invalid device code challenge was not cancelled")
			return
		}
		writeAppServerResponse(t, serverConn, cancel.ID, map[string]string{"status": "canceled"})
		serverDone <- nil
	}()
	if _, err := client.StartDeviceCodeLogin(testAppServerContext(t)); err == nil {
		t.Fatal("StartDeviceCodeLogin() accepted markup as a device code")
	}
	select {
	case err := <-serverDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("device login cancellation was not sent after unsafe code")
	}
}

func TestStartDeviceCodeLoginReportsUnknownCleanupWhenInvalidChallengeCannotBeCancelled(t *testing.T) {
	client, serverConn := newFakeAppServerClient(t)
	serverDone := make(chan error, 1)
	go func() {
		initializeFakeAppServer(t, serverConn)
		request := readAppServerRequest(t, serverConn)
		writeAppServerResponse(t, serverConn, request.ID, map[string]string{
			"type":            "chatgptDeviceCode",
			"loginId":         "b8675a78-9052-4f83-b76a-8ae4f5f44821",
			"verificationUrl": "https://attacker.invalid/codex/device",
			"userCode":        "ABCD-EFGH",
		})
		cancel := readAppServerRequest(t, serverConn)
		writeAppServerResponse(t, serverConn, cancel.ID, map[string]string{"status": "unexpected"})
		serverDone <- nil
	}()

	_, err := client.StartDeviceCodeLogin(testAppServerContext(t))
	if err == nil || !strings.Contains(err.Error(), "cancellation outcome is unknown") {
		t.Fatalf("StartDeviceCodeLogin() error = %v, want unknown cancellation outcome", err)
	}
	if strings.Contains(err.Error(), "ABCD-EFGH") {
		t.Fatalf("StartDeviceCodeLogin() leaked one-time code in error: %v", err)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}

func TestDeviceCodeChallengeFormattingRedactsOneTimeCode(t *testing.T) {
	challenge := DeviceCodeChallenge{
		LoginID:         "b8675a78-9052-4f83-b76a-8ae4f5f44821",
		VerificationURL: "https://auth.openai.com/codex/device",
		UserCode:        "ABCD-EFGH",
	}
	for _, format := range []string{"%v", "%+v", "%#v"} {
		formatted := fmt.Sprintf(format, challenge)
		if strings.Contains(formatted, challenge.UserCode) {
			t.Fatalf("format %s leaked one-time device code: %s", format, formatted)
		}
	}
}

func TestCancelDeviceCodeLoginRejectsEmptyIDWithoutRPC(t *testing.T) {
	client, serverConn := newFakeAppServerClient(t)
	defer serverConn.Close()
	if _, err := client.CancelDeviceCodeLogin(context.Background(), " \t "); err == nil {
		t.Fatal("CancelDeviceCodeLogin() accepted an empty login ID")
	}
}

func TestStartDeviceCodeLoginRejectsWrongResponseVariant(t *testing.T) {
	client, serverConn := newFakeAppServerClient(t)
	serverDone := make(chan error, 1)
	go func() {
		initializeFakeAppServer(t, serverConn)
		request := readAppServerRequest(t, serverConn)
		response, _ := json.Marshal(map[string]string{"type": "chatgpt", "loginId": "b8675a78-9052-4f83-b76a-8ae4f5f44821"})
		writeAppServerResponse(t, serverConn, request.ID, json.RawMessage(response))
		cancel := readAppServerRequest(t, serverConn)
		if cancel.Method != "account/login/cancel" || cancel.Params["loginId"] != "b8675a78-9052-4f83-b76a-8ae4f5f44821" {
			serverDone <- errorsForTest("unexpected login variant was not cancelled")
			return
		}
		writeAppServerResponse(t, serverConn, cancel.ID, map[string]string{"status": "canceled"})
		serverDone <- nil
	}()
	if _, err := client.StartDeviceCodeLogin(testAppServerContext(t)); err == nil {
		t.Fatal("StartDeviceCodeLogin() accepted the browser-login response variant")
	}
	select {
	case err := <-serverDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("unexpected login variant was not cancelled")
	}
}
