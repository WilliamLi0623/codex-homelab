package codexsession

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

func TestProtocolClientCorrelatesCallAndDeliversNotifications(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	client := NewProtocolClient(clientConn, clientConn)
	defer client.Close()
	defer serverConn.Close()
	if err := client.Start(); err != nil {
		t.Fatal(err)
	}

	serverDone := make(chan error, 1)
	go func() {
		defer close(serverDone)
		scanner := bufio.NewScanner(serverConn)
		if !scanner.Scan() {
			serverDone <- scanner.Err()
			return
		}
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			serverDone <- err
			return
		}
		if request.Method != "test/read" {
			serverDone <- unexpectedMethodError(request.Method)
			return
		}
		if _, err := serverConn.Write([]byte(`{"method":"turn/started","params":{"turnId":"turn-1"}}` + "\n")); err != nil {
			serverDone <- err
			return
		}
		_, err := serverConn.Write(append(append([]byte(`{"id":`), request.ID...), []byte(`,"result":{"value":"ok"}}`+"\n")...))
		serverDone <- err
	}()

	var result struct {
		Value string `json:"value"`
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := client.Call(ctx, "test/read", map[string]string{"input": "x"}, &result); err != nil {
		t.Fatal(err)
	}
	if result.Value != "ok" {
		t.Fatalf("result=%q, want ok", result.Value)
	}
	select {
	case event := <-client.Events():
		if event.Method != "turn/started" || string(event.Params) != `{"turnId":"turn-1"}` {
			t.Fatalf("event=%+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("notification was not delivered")
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}

func TestProtocolClientMultiplexesConcurrentCalls(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	client := NewProtocolClient(clientConn, clientConn)
	defer client.Close()
	defer serverConn.Close()
	if err := client.Start(); err != nil {
		t.Fatal(err)
	}

	type echoRequest struct {
		ID     json.RawMessage `json:"id"`
		IDRaw  string          `json:"-"`
		Params struct {
			Value string `json:"value"`
		} `json:"params"`
	}
	serverDone := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(serverConn)
		requests := make([]echoRequest, 0, 2)
		for len(requests) < 2 && scanner.Scan() {
			var request echoRequest
			if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
				serverDone <- err
				return
			}
			request.IDRaw = string(request.ID)
			requests = append(requests, request)
		}
		if len(requests) != 2 {
			serverDone <- scanner.Err()
			return
		}
		for i := len(requests) - 1; i >= 0; i-- {
			line, err := json.Marshal(map[string]any{"id": json.RawMessage(requests[i].IDRaw), "result": map[string]string{"value": requests[i].Params.Value}})
			if err != nil {
				serverDone <- err
				return
			}
			if _, err := serverConn.Write(append(line, '\n')); err != nil {
				serverDone <- err
				return
			}
		}
		serverDone <- nil
	}()

	type result struct {
		Value string `json:"value"`
	}
	results := make(chan result, 2)
	errs := make(chan error, 2)
	for _, value := range []string{"one", "two"} {
		go func(value string) {
			var got result
			err := client.Call(context.Background(), "test/echo", map[string]string{"value": value}, &got)
			if err != nil {
				errs <- err
				return
			}
			results <- got
		}(value)
	}
	seen := map[string]bool{}
	for range 2 {
		select {
		case err := <-errs:
			t.Fatal(err)
		case got := <-results:
			seen[got.Value] = true
		case <-time.After(time.Second):
			t.Fatal("concurrent calls did not complete")
		}
	}
	if !seen["one"] || !seen["two"] {
		t.Fatalf("responses were mis-correlated: %+v", seen)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}

func TestProtocolClientSurfacesServerRequestAndRespondsWithOriginalID(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	client := NewProtocolClient(clientConn, clientConn)
	defer client.Close()
	defer serverConn.Close()
	if err := client.Start(); err != nil {
		t.Fatal(err)
	}

	serverDone := make(chan json.RawMessage, 1)
	go func() {
		_, _ = serverConn.Write([]byte(`{"id":73,"method":"item/commandExecution/requestApproval","params":{"threadId":"thread-1"}}` + "\n"))
		line, _ := bufio.NewReader(serverConn).ReadBytes('\n')
		var response struct {
			ID     json.RawMessage `json:"id"`
			Result json.RawMessage `json:"result"`
		}
		if err := json.Unmarshal(line, &response); err != nil {
			serverDone <- nil
			return
		}
		serverDone <- response.ID
		if string(response.ID) != "73" || string(response.Result) != `{"decision":"decline"}` {
			serverDone <- nil
		}
	}()

	select {
	case request := <-client.ServerRequests():
		if request.Method != "item/commandExecution/requestApproval" || string(request.ID) != "73" {
			t.Fatalf("server request=%+v", request)
		}
		if err := client.Respond(context.Background(), request.ID, map[string]string{"decision": "decline"}); err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("approval request was not delivered")
	}
	if id := <-serverDone; string(id) != "73" {
		t.Fatalf("response id=%s, want original server request ID 73", id)
	}
}

func TestProtocolClientCancelsPendingCallWithoutPoisoningTheStream(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	client := NewProtocolClient(clientConn, clientConn)
	defer client.Close()
	defer serverConn.Close()
	if err := client.Start(); err != nil {
		t.Fatal(err)
	}

	requestRead := make(chan json.RawMessage, 1)
	go func() {
		line, _ := bufio.NewReader(serverConn).ReadBytes('\n')
		var request struct {
			ID json.RawMessage `json:"id"`
		}
		if json.Unmarshal(line, &request) == nil {
			requestRead <- request.ID
		}
	}()
	ctx, cancel := context.WithCancel(context.Background())
	callDone := make(chan error, 1)
	go func() { callDone <- client.Call(ctx, "test/wait", map[string]string{}, nil) }()
	id := <-requestRead
	cancel()
	if err := <-callDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("call error=%v, want context cancellation", err)
	}
	line, _ := json.Marshal(map[string]any{"id": id, "result": map[string]bool{"late": true}})
	if _, err := serverConn.Write(append(line, '\n')); err != nil {
		t.Fatal(err)
	}
	select {
	case <-client.Done():
		t.Fatalf("late response closed the healthy stream: %v", client.Err())
	case <-time.After(20 * time.Millisecond):
	}
}

func TestProtocolClientSanitizesRPCErrorText(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	client := NewProtocolClient(clientConn, clientConn)
	defer client.Close()
	defer serverConn.Close()
	if err := client.Start(); err != nil {
		t.Fatal(err)
	}
	go func() {
		line, _ := bufio.NewReader(serverConn).ReadBytes('\n')
		var request struct {
			ID json.RawMessage `json:"id"`
		}
		_ = json.Unmarshal(line, &request)
		response, _ := json.Marshal(map[string]any{"id": request.ID, "error": map[string]any{"code": 401, "message": "secret-prompt-content"}})
		_, _ = serverConn.Write(append(response, '\n'))
	}()
	err := client.Call(context.Background(), "test/fails", map[string]string{}, nil)
	var rpcErr *ProtocolRPCError
	if !errors.As(err, &rpcErr) || rpcErr.Code != 401 {
		t.Fatalf("call error=%v, want ProtocolRPCError code 401", err)
	}
	if strings.Contains(err.Error(), "secret-prompt-content") {
		t.Fatalf("RPC error exposed provider message: %v", err)
	}
}

func TestProtocolClientStopsOnMalformedOrOversizedMessage(t *testing.T) {
	for _, test := range []struct {
		name    string
		payload []byte
	}{
		{name: "malformed JSON", payload: []byte("{not-json}\n")},
		{name: "oversized line", payload: []byte(strings.Repeat("x", maxProtocolLineBytes+1) + "\n")},
	} {
		t.Run(test.name, func(t *testing.T) {
			clientConn, serverConn := net.Pipe()
			client := NewProtocolClient(clientConn, clientConn)
			defer client.Close()
			defer serverConn.Close()
			if err := client.Start(); err != nil {
				t.Fatal(err)
			}
			writeDone := make(chan struct{})
			go func() {
				_, _ = serverConn.Write(test.payload)
				close(writeDone)
			}()
			select {
			case <-client.Done():
				if client.Err() == nil {
					t.Fatal("protocol failure did not record an error")
				}
			case <-time.After(2 * time.Second):
				t.Fatal("protocol client did not stop on invalid message")
			}
			_ = client.Close()
			<-writeDone
		})
	}
}

type unexpectedMethodError string

func (e unexpectedMethodError) Error() string { return "unexpected method " + string(e) }
