package codexsession

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
)

const (
	maxProtocolLineBytes = 8 << 20
	eventQueueSize       = 256
	requestQueueSize     = 32
)

var ErrProtocolClosed = errors.New("App Server protocol client closed")

// Event is a server notification that does not require a JSON-RPC response.
type Event struct {
	Method string
	Params json.RawMessage
}

// ServerRequest is a server-initiated JSON-RPC request. The caller must answer
// it explicitly with Respond; this client never auto-approves a request.
type ServerRequest struct {
	ID     json.RawMessage
	Method string
	Params json.RawMessage
}

// ProtocolRPCError intentionally omits provider-supplied error text, which may
// contain sensitive request details. Callers can classify the numeric code.
type ProtocolRPCError struct {
	Method string
	Code   int
}

func (e *ProtocolRPCError) Error() string {
	return fmt.Sprintf("App Server RPC %s failed with code %d", e.Method, e.Code)
}

type callResult struct {
	result json.RawMessage
	err    error
}

type rpcMessage struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code int `json:"code"`
	} `json:"error"`
}

type rpcCall struct {
	method string
	result chan callResult
}

// ProtocolClient multiplexes concurrent JSON-RPC calls over one App Server
// stdio stream and dispatches notifications and server requests from one
// reader goroutine.
type ProtocolClient struct {
	reader io.ReadCloser
	writer io.Writer

	writeMu     sync.Mutex
	mu          sync.Mutex
	pending     map[string]rpcCall
	nextID      atomic.Uint64
	closed      bool
	closeOnce   sync.Once
	started     sync.Once
	startedFlag atomic.Bool
	startErr    error
	err         error
	done        chan struct{}
	events      chan Event
	requests    chan ServerRequest
}

func NewProtocolClient(reader io.ReadCloser, writer io.Writer) *ProtocolClient {
	return &ProtocolClient{
		reader:   reader,
		writer:   writer,
		pending:  make(map[string]rpcCall),
		done:     make(chan struct{}),
		events:   make(chan Event, eventQueueSize),
		requests: make(chan ServerRequest, requestQueueSize),
	}
}

func (c *ProtocolClient) Start() error {
	c.started.Do(func() {
		c.mu.Lock()
		closed := c.closed
		c.mu.Unlock()
		if closed {
			c.startErr = ErrProtocolClosed
			return
		}
		c.startedFlag.Store(true)
		go c.readLoop()
	})
	return c.startErr
}

func (c *ProtocolClient) Events() <-chan Event { return c.events }

func (c *ProtocolClient) ServerRequests() <-chan ServerRequest { return c.requests }

func (c *ProtocolClient) Done() <-chan struct{} { return c.done }

func (c *ProtocolClient) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

func (c *ProtocolClient) Call(ctx context.Context, method string, params, result any) error {
	if err := c.Start(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if method == "" {
		return errors.New("App Server RPC method is required")
	}
	id := c.nextID.Add(1)
	idRaw, _ := json.Marshal(id)
	key := string(idRaw)
	call := rpcCall{method: method, result: make(chan callResult, 1)}
	c.mu.Lock()
	if c.closed {
		err := c.err
		if err == nil {
			err = ErrProtocolClosed
		}
		c.mu.Unlock()
		return err
	}
	c.pending[key] = call
	c.mu.Unlock()

	request := struct {
		ID     uint64 `json:"id"`
		Method string `json:"method"`
		Params any    `json:"params"`
	}{ID: id, Method: method, Params: params}
	if err := c.writeJSON(request); err != nil {
		c.removePending(key)
		return fmt.Errorf("write App Server RPC %s: %w", method, err)
	}

	select {
	case response := <-call.result:
		if response.err != nil {
			return response.err
		}
		if result == nil || len(response.result) == 0 || string(response.result) == "null" {
			return nil
		}
		if err := json.Unmarshal(response.result, result); err != nil {
			return fmt.Errorf("decode App Server RPC %s result: %w", method, err)
		}
		return nil
	case <-ctx.Done():
		c.removePending(key)
		return ctx.Err()
	case <-c.done:
		c.removePending(key)
		if err := c.Err(); err != nil {
			return err
		}
		return ErrProtocolClosed
	}
}

func (c *ProtocolClient) Notify(ctx context.Context, method string, params any) error {
	if err := c.Start(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if method == "" {
		return errors.New("App Server notification method is required")
	}
	return c.writeJSON(struct {
		Method string `json:"method"`
		Params any    `json:"params"`
	}{Method: method, Params: params})
}

func (c *ProtocolClient) Respond(ctx context.Context, id json.RawMessage, result any) error {
	if err := c.Start(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(id) == 0 || !json.Valid(id) || string(id) == "null" {
		return errors.New("App Server server-request ID is invalid")
	}
	return c.writeJSON(struct {
		ID     json.RawMessage `json:"id"`
		Result any             `json:"result"`
	}{ID: id, Result: result})
}

// RespondError explicitly rejects a server-initiated request. It is used for
// approval request shapes the UI cannot safely represent; it never grants
// permissions or executes a command.
func (c *ProtocolClient) RespondError(ctx context.Context, id json.RawMessage, code int, message string) error {
	if err := c.Start(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(id) == 0 || !json.Valid(id) || string(id) == "null" {
		return errors.New("App Server server-request ID is invalid")
	}
	if code == 0 || message == "" {
		return errors.New("App Server rejection requires an error code and message")
	}
	return c.writeJSON(struct {
		ID    json.RawMessage `json:"id"`
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}{ID: id, Error: struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}{Code: code, Message: message}})
}

func (c *ProtocolClient) Close() error {
	c.closeOnce.Do(func() {
		c.mu.Lock()
		c.closed = true
		c.mu.Unlock()
		_ = c.reader.Close()
	})
	if c.startedFlag.Load() {
		<-c.done
	}
	return nil
}

func (c *ProtocolClient) readLoop() {
	var terminalErr error
	defer func() {
		if terminalErr == nil {
			terminalErr = io.EOF
		}
		c.mu.Lock()
		c.closed = true
		c.err = terminalErr
		for key, pending := range c.pending {
			pending.result <- callResult{err: terminalErr}
			delete(c.pending, key)
		}
		close(c.events)
		close(c.requests)
		close(c.done)
		c.mu.Unlock()
	}()

	scanner := bufio.NewScanner(c.reader)
	scanner.Buffer(make([]byte, 64*1024), maxProtocolLineBytes)
	for scanner.Scan() {
		var message rpcMessage
		if err := json.Unmarshal(scanner.Bytes(), &message); err != nil {
			terminalErr = fmt.Errorf("decode App Server JSON-RPC message: %w", err)
			return
		}
		if message.Method != "" {
			if len(message.ID) != 0 && string(message.ID) != "null" {
				request := ServerRequest{ID: append(json.RawMessage(nil), message.ID...), Method: message.Method, Params: message.Params}
				select {
				case c.requests <- request:
				default:
					terminalErr = errors.New("App Server request queue is full")
					return
				}
			} else {
				event := Event{Method: message.Method, Params: message.Params}
				select {
				case c.events <- event:
				default:
					terminalErr = errors.New("App Server event queue is full")
					return
				}
			}
			continue
		}
		if len(message.ID) == 0 || !json.Valid(message.ID) {
			terminalErr = errors.New("App Server JSON-RPC message has neither a method nor a valid ID")
			return
		}
		key := string(message.ID)
		c.mu.Lock()
		pending, ok := c.pending[key]
		if ok {
			delete(c.pending, key)
		}
		c.mu.Unlock()
		if !ok {
			continue
		}
		if message.Error != nil {
			pending.result <- callResult{err: &ProtocolRPCError{Method: pending.method, Code: message.Error.Code}}
			continue
		}
		pending.result <- callResult{result: message.Result}
	}
	if err := scanner.Err(); err != nil {
		terminalErr = fmt.Errorf("read App Server JSON-RPC stream: %w", err)
	}
}

func (c *ProtocolClient) writeJSON(value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode App Server JSON-RPC message: %w", err)
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	c.mu.Lock()
	closed := c.closed
	err = c.err
	c.mu.Unlock()
	if closed {
		if err != nil {
			return err
		}
		return ErrProtocolClosed
	}
	if _, err := c.writer.Write(append(encoded, '\n')); err != nil {
		return fmt.Errorf("write App Server JSON-RPC stream: %w", err)
	}
	return nil
}

func (c *ProtocolClient) removePending(key string) {
	c.mu.Lock()
	delete(c.pending, key)
	c.mu.Unlock()
}
