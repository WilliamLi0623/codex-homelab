package codexsession

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"sort"
	"sync"
	"time"
)

const sessionNoticeBuffer = 256

type RoutePin struct {
	Mode       string    `json:"mode"`
	Provider   string    `json:"provider"`
	Model      string    `json:"model"`
	Effort     string    `json:"effort"`
	Generation uint64    `json:"generation"`
	CreatedAt  time.Time `json:"createdAt"`
}

type SessionNotice struct {
	Event     *Event           `json:"event,omitempty"`
	Approval  *ApprovalRequest `json:"approval,omitempty"`
	Error     string           `json:"error,omitempty"`
	Supported bool             `json:"supported,omitempty"`
}

type ManagedSession struct {
	Thread                 AppServerThread
	Pin                    RoutePin
	Notices                chan SessionNotice
	streamFailed           bool
	activeTurnID           string
	turnStarting           bool
	completedWhileStarting string
	turnStateUnknown       bool
}

type pendingApproval struct {
	request    ServerRequest
	thread     string
	responding bool
}

// SessionManager keeps only routing pins and pending approval callbacks in
// memory. Conversation history remains owned by App Server.
type SessionManager struct {
	client *AppServerClient
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}

	mu        sync.RWMutex
	closed    bool
	sessions  map[string]*ManagedSession
	approvals map[string]pendingApproval
}

func NewSessionManager(client *AppServerClient) (*SessionManager, error) {
	if client == nil || client.protocol == nil {
		return nil, errors.New("App Server client is required")
	}
	if err := client.protocol.Start(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	manager := &SessionManager{
		client: client, ctx: ctx, cancel: cancel, done: make(chan struct{}),
		sessions: make(map[string]*ManagedSession), approvals: make(map[string]pendingApproval),
	}
	go manager.dispatch()
	return manager, nil
}

func (m *SessionManager) CreateThread(ctx context.Context, options ThreadStartOptions, pin RoutePin) (*ManagedSession, error) {
	if options.ModelProvider != pin.Provider || options.Model != pin.Model || options.Effort != pin.Effort {
		return nil, errors.New("thread options do not match the route pin")
	}
	thread, err := m.client.StartThread(ctx, options)
	if err != nil {
		return nil, err
	}
	return m.RegisterThread(thread, pin)
}

func (m *SessionManager) RegisterThread(thread AppServerThread, pin RoutePin) (*ManagedSession, error) {
	if thread.ID == "" || thread.ThreadSource == nil || *thread.ThreadSource != sessionUIServiceName {
		return nil, errors.New("thread is not marked as a local Codex session UI thread")
	}
	if (pin.Mode != "normal" && pin.Mode != "quota_fallback") || pin.Provider == "" || pin.Model == "" || pin.Effort == "" || thread.ModelProvider != pin.Provider || thread.Model != pin.Model || thread.ReasoningEffort == nil || *thread.ReasoningEffort != pin.Effort {
		return nil, errors.New("thread metadata does not match its route pin")
	}
	if pin.CreatedAt.IsZero() {
		pin.CreatedAt = time.Now().UTC()
	}
	session := &ManagedSession{Thread: thread, Pin: pin, Notices: make(chan SessionNotice, sessionNoticeBuffer)}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, errors.New("session manager is closed")
	}
	if _, exists := m.sessions[thread.ID]; exists {
		return nil, errors.New("thread is already registered")
	}
	m.sessions[thread.ID] = session
	return session, nil
}

func RoutePinFromThread(thread AppServerThread) (RoutePin, error) {
	if thread.ThreadSource == nil || *thread.ThreadSource != sessionUIServiceName || thread.ID == "" || thread.ReasoningEffort == nil {
		return RoutePin{}, errors.New("thread does not contain trusted local session route metadata")
	}
	pin := RoutePin{Provider: thread.ModelProvider, Model: thread.Model, Effort: *thread.ReasoningEffort}
	switch {
	case thread.ModelProvider == "openai" && thread.Model == "gpt-6-luna" && *thread.ReasoningEffort == "high":
		pin.Mode = "normal"
	case thread.ModelProvider == "osc" && thread.Model == "muse-spark-1.3-contributor" && *thread.ReasoningEffort == "xhigh":
		pin.Mode = "quota_fallback"
	default:
		return RoutePin{}, errors.New("thread provider/model/effort tuple is not a known session route")
	}
	if thread.CreatedAt > 0 {
		pin.CreatedAt = time.Unix(thread.CreatedAt, 0).UTC()
	}
	return pin, nil
}

func (m *SessionManager) RecoverListedThreads(ctx context.Context) ([]*ManagedSession, error) {
	var recovered []*ManagedSession
	cursor := ""
	seenCursors := make(map[string]struct{})
	for {
		page, err := m.client.ListThreads(ctx, 100, cursor)
		if err != nil {
			return nil, err
		}
		for _, thread := range page.Threads {
			if thread.ThreadSource == nil || *thread.ThreadSource != sessionUIServiceName {
				continue
			}
			pin, err := RoutePinFromThread(thread)
			if err != nil {
				return nil, err
			}
			resumed, err := m.client.ResumeThread(ctx, thread.ID)
			if err != nil {
				return nil, err
			}
			if resumed.ModelProvider != pin.Provider || resumed.Model != pin.Model || resumed.ReasoningEffort == nil || *resumed.ReasoningEffort != pin.Effort || resumed.ThreadSource == nil || *resumed.ThreadSource != sessionUIServiceName {
				return nil, errors.New("resumed App Server thread changed its route metadata")
			}
			session, err := m.RegisterThread(resumed, pin)
			if err != nil {
				if existing := m.Get(thread.ID); existing != nil && existing.Pin.Provider == pin.Provider && existing.Pin.Model == pin.Model && existing.Pin.Effort == pin.Effort {
					session = existing
				} else {
					return nil, err
				}
			}
			recovered = append(recovered, session)
		}
		if page.NextCursor == "" {
			return recovered, nil
		}
		if _, exists := seenCursors[page.NextCursor]; exists {
			return nil, errors.New("App Server thread list repeated a pagination cursor")
		}
		seenCursors[page.NextCursor] = struct{}{}
		cursor = page.NextCursor
	}
}

func (m *SessionManager) Get(threadID string) *ManagedSession {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.sessions[threadID]
}

func (m *SessionManager) List() []*ManagedSession {
	m.mu.RLock()
	defer m.mu.RUnlock()
	sessions := make([]*ManagedSession, 0, len(m.sessions))
	for _, session := range m.sessions {
		sessions = append(sessions, session)
	}
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].Pin.CreatedAt.After(sessions[j].Pin.CreatedAt) })
	return sessions
}

func (m *SessionManager) ResumeThread(ctx context.Context, threadID string) (*ManagedSession, error) {
	m.mu.RLock()
	session := m.sessions[threadID]
	m.mu.RUnlock()
	if session == nil {
		return nil, errors.New("thread is not registered with this local session manager")
	}
	thread, err := m.client.ResumeThread(ctx, threadID)
	if err != nil {
		return nil, err
	}
	if thread.ThreadSource == nil || *thread.ThreadSource != sessionUIServiceName || thread.ModelProvider != session.Pin.Provider || thread.Model != session.Pin.Model || thread.ReasoningEffort == nil || *thread.ReasoningEffort != session.Pin.Effort {
		return nil, errors.New("resumed thread metadata does not match its pinned route")
	}
	return session, nil
}

func (m *SessionManager) StartTurn(ctx context.Context, threadID, text string) (Turn, error) {
	session := m.Get(threadID)
	if session == nil {
		return Turn{}, errors.New("thread is not registered with this local session manager")
	}
	m.mu.Lock()
	if session.turnStarting || session.activeTurnID != "" || session.turnStateUnknown {
		m.mu.Unlock()
		return Turn{}, errors.New("session has an active or ambiguous turn outcome")
	}
	session.turnStarting = true
	m.mu.Unlock()
	turn, err := m.client.StartTurn(ctx, threadID, text)
	m.mu.Lock()
	session.turnStarting = false
	if err == nil {
		if session.completedWhileStarting == turn.ID {
			session.completedWhileStarting = ""
		} else {
			session.activeTurnID = turn.ID
		}
	} else {
		session.turnStateUnknown = true
	}
	m.mu.Unlock()
	return turn, err
}

func (m *SessionManager) InterruptTurn(ctx context.Context, threadID, turnID string) error {
	session := m.Get(threadID)
	if session == nil {
		return errors.New("thread is not registered with this local session manager")
	}
	m.mu.RLock()
	active := session.activeTurnID == turnID && !session.turnStarting
	m.mu.RUnlock()
	if !active {
		return errors.New("turn is not the active turn for this session")
	}
	return m.client.InterruptTurn(ctx, threadID, turnID)
}

func (m *SessionManager) RespondApproval(ctx context.Context, approval ApprovalRequest, decision string) error {
	key := string(approval.RequestID)
	m.mu.Lock()
	pending, ok := m.approvals[key]
	if !ok || pending.thread != approval.ThreadID || approval.Method != pending.request.Method {
		m.mu.Unlock()
		return errors.New("approval request is no longer pending for this thread")
	}
	if pending.responding {
		m.mu.Unlock()
		return errors.New("approval response is already in progress")
	}
	result, err := BuildApprovalResponse(pending.request.Method, pending.request.Params, decision)
	if err != nil {
		m.mu.Unlock()
		return err
	}
	pending.responding = true
	m.approvals[key] = pending
	m.mu.Unlock()
	if err := m.client.Respond(ctx, pending.request, result); err != nil {
		m.mu.Lock()
		delete(m.approvals, key)
		m.mu.Unlock()
		return err
	}
	m.mu.Lock()
	delete(m.approvals, key)
	m.mu.Unlock()
	return nil
}

func (m *SessionManager) RespondApprovalID(ctx context.Context, threadID, publicID, decision string) error {
	rawID, err := base64.RawURLEncoding.DecodeString(publicID)
	if err != nil || !json.Valid(rawID) || string(rawID) == "null" {
		return errors.New("approval identifier is invalid")
	}
	m.mu.RLock()
	pending, ok := m.approvals[string(rawID)]
	m.mu.RUnlock()
	if !ok || pending.thread != threadID {
		return errors.New("approval request is no longer pending for this thread")
	}
	approval, err := ParseApprovalRequest(pending.request)
	if err != nil {
		return err
	}
	return m.RespondApproval(ctx, approval, decision)
}

func (m *SessionManager) Close() {
	m.cancel()
	<-m.done
}

func (m *SessionManager) dispatch() {
	defer close(m.done)
	events := m.client.Events()
	requests := m.client.ServerRequests()
	for events != nil || requests != nil {
		select {
		case <-m.ctx.Done():
			m.closeSessions()
			return
		case event, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			m.forwardEvent(event)
		case request, ok := <-requests:
			if !ok {
				requests = nil
				continue
			}
			m.forwardRequest(request)
		}
	}
	m.closeSessionsWithError("Codex App Server disconnected; the session outcome may be incomplete")
}

func (m *SessionManager) forwardEvent(event Event) {
	threadID := eventThreadID(event.Params)
	if threadID == "" {
		return
	}
	m.mu.Lock()
	session := m.sessions[threadID]
	if session != nil && event.Method == "turn/completed" {
		var completed struct {
			Turn struct {
				ID string `json:"id"`
			} `json:"turn"`
		}
		if json.Unmarshal(event.Params, &completed) == nil && completed.Turn.ID != "" {
			if session.turnStarting {
				session.completedWhileStarting = completed.Turn.ID
			}
			if session.activeTurnID == completed.Turn.ID {
				session.activeTurnID = ""
			}
		}
	}
	m.mu.Unlock()
	if session == nil {
		return
	}
	if session.streamFailed {
		return
	}
	copy := event
	copy.Params = append(json.RawMessage(nil), event.Params...)
	select {
	case session.Notices <- SessionNotice{Event: &copy}:
	default:
		m.failSession(threadID, "App Server event queue is full; session stream stopped")
	}
}

func (m *SessionManager) forwardRequest(request ServerRequest) {
	var thread struct {
		ThreadID string `json:"threadId"`
	}
	_ = json.Unmarshal(request.Params, &thread)
	if thread.ThreadID == "" {
		_ = m.client.Reject(m.ctx, request, -32602, "server request is missing its thread identifier")
		return
	}
	approval, parseErr := ParseApprovalRequest(request)
	m.mu.Lock()
	session := m.sessions[thread.ThreadID]
	if session != nil && parseErr == nil {
		m.approvals[string(request.ID)] = pendingApproval{request: request, thread: thread.ThreadID}
	}
	m.mu.Unlock()
	if session == nil {
		_ = m.client.Reject(m.ctx, request, -32001, "thread is not managed by the local session UI")
		return
	}
	notice := SessionNotice{Approval: &approval, Supported: parseErr == nil}
	if parseErr != nil {
		notice.Error = parseErr.Error()
		if err := m.client.Reject(m.ctx, request, -32000, "approval request is not supported by the local session UI"); err != nil {
			notice.Error = "unsupported approval request could not be rejected"
		}
	}
	select {
	case session.Notices <- notice:
	default:
		m.failSession(thread.ThreadID, "App Server approval queue is full; session stream stopped")
	}
}

func (m *SessionManager) failSession(threadID, message string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if session := m.sessions[threadID]; session != nil {
		enqueueSessionFailure(session, message)
	}
}

func (m *SessionManager) closeSessions() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	m.closed = true
	for _, session := range m.sessions {
		close(session.Notices)
	}
}

func (m *SessionManager) closeSessionsWithError(message string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	m.closed = true
	for _, session := range m.sessions {
		enqueueSessionFailure(session, message)
		close(session.Notices)
	}
}

func enqueueSessionFailure(session *ManagedSession, message string) {
	if session.streamFailed {
		return
	}
	session.streamFailed = true
	notice := SessionNotice{Error: message}
	select {
	case session.Notices <- notice:
	default:
		// The consumer is already behind. Drop the oldest buffered event so the
		// terminal failure cannot become a silent successful close.
		select {
		case <-session.Notices:
		default:
		}
		select {
		case session.Notices <- notice:
		default:
		}
	}
}

func eventThreadID(params json.RawMessage) string {
	var envelope struct {
		ThreadID string `json:"threadId"`
	}
	if len(params) == 0 || json.Unmarshal(params, &envelope) != nil {
		return ""
	}
	return envelope.ThreadID
}
