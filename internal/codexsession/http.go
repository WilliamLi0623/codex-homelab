package codexsession

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/WilliamLi0623/codex-homelab/internal/codexrouting"
)

const (
	sessionUICookieName = "codex_session_ui"
	maxSessionBodyBytes = 128 << 10
	maxTurnTextBytes    = 64 << 10
	maxVisibleDelta     = 32 << 10
)

type SessionHTTPConfig struct {
	ListenAddress   string
	Origin          string
	Token           string
	StaticFiles     fs.FS
	MaxSSE          int
	FallbackEnabled bool
}

type SessionRouteCoordinator interface {
	CurrentDecision(context.Context) (codexrouting.RouteDecision, error)
	RefreshAndDecide(context.Context) (codexrouting.RouteDecision, error)
}

type SessionBackend interface {
	CreateThread(context.Context, ThreadStartOptions, RoutePin) (*ManagedSession, error)
	Get(string) *ManagedSession
	List() []*ManagedSession
	StartTurn(context.Context, string, string) (Turn, error)
	InterruptTurn(context.Context, string, string) error
	RespondApprovalID(context.Context, string, string, string) error
}

type SessionHistoryBackend interface {
	History(context.Context, string) (SessionHistory, error)
}

type SessionRoute struct {
	Provider string
	Model    string
	Effort   string
}

type SessionHTTPHandler struct {
	config      SessionHTTPConfig
	coordinator SessionRouteCoordinator
	sessions    SessionBackend
	static      http.Handler
	mux         *http.ServeMux
	sseMu       sync.Mutex
	sseActive   int
	sseByThread map[string]int
}

func SessionRouteForMode(mode codexrouting.Mode) (SessionRoute, error) {
	switch mode {
	case codexrouting.ModeNormal:
		return SessionRoute{Provider: "openai", Model: "gpt-6-luna", Effort: "high"}, nil
	case codexrouting.ModeQuotaFallback:
		return SessionRoute{Provider: "osc", Model: "muse-spark-1.3-contributor", Effort: "xhigh"}, nil
	default:
		return SessionRoute{}, errors.New("unknown session route mode")
	}
}

func NewSessionHTTPHandler(config SessionHTTPConfig, coordinator SessionRouteCoordinator, sessions SessionBackend) (http.Handler, error) {
	if err := validateSessionHTTPConfig(config); err != nil {
		return nil, err
	}
	if coordinator == nil || sessions == nil {
		return nil, errors.New("session route coordinator and backend are required")
	}
	h := &SessionHTTPHandler{config: config, coordinator: coordinator, sessions: sessions, mux: http.NewServeMux(), sseByThread: make(map[string]int)}
	if config.StaticFiles != nil {
		h.static = http.FileServer(http.FS(config.StaticFiles))
	}
	h.mux.HandleFunc("GET /healthz", h.health)
	h.mux.HandleFunc("GET /api/status", h.status)
	h.mux.HandleFunc("GET /api/sessions", h.listSessions)
	h.mux.HandleFunc("GET /api/sessions/{id}/history", h.sessionHistory)
	h.mux.HandleFunc("POST /api/sessions", h.createSession)
	h.mux.HandleFunc("GET /api/sessions/{id}/events", h.streamSession)
	h.mux.HandleFunc("POST /api/sessions/{id}/turns", h.startTurn)
	h.mux.HandleFunc("POST /api/sessions/{id}/interrupt", h.interruptTurn)
	h.mux.HandleFunc("POST /api/approvals", h.respondApproval)
	h.mux.HandleFunc("/", h.staticOrNotFound)
	return h, nil
}

func (h *SessionHTTPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) { h.serve(w, r) }

func validateSessionHTTPConfig(config SessionHTTPConfig) error {
	host, portText, err := net.SplitHostPort(config.ListenAddress)
	if err != nil || host != "127.0.0.1" {
		return errors.New("session UI listener must bind to 127.0.0.1 only")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return errors.New("session UI listener port must be between 1 and 65535")
	}
	origin, err := url.Parse(config.Origin)
	if err != nil || origin.Scheme != "http" || origin.Host != net.JoinHostPort(host, portText) || origin.User != nil || origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" {
		return errors.New("session UI origin must exactly match its loopback listener")
	}
	if len(config.Token) < 32 {
		return errors.New("session UI token must contain at least 32 bytes")
	}
	if config.MaxSSE < 1 || config.MaxSSE > 64 {
		return errors.New("session UI SSE limit must be between 1 and 64")
	}
	return nil
}

func (h *SessionHTTPHandler) serve(w http.ResponseWriter, r *http.Request) {
	setSecurityHeaders(w)
	if r.Host != strings.TrimPrefix(h.config.Origin, "http://") {
		writeHTTPError(w, http.StatusForbidden, "host is not allowed")
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/healthz" {
		if r.URL.Path != "/healthz" && !h.authorized(r) {
			writeHTTPError(w, http.StatusUnauthorized, "local session authorization required")
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if r.Header.Get("Origin") != h.config.Origin {
				writeHTTPError(w, http.StatusForbidden, "same-origin request required")
				return
			}
			w.Header().Set("Cache-Control", "no-store")
		}
	}
	h.mux.ServeHTTP(w, r)
}

func (h *SessionHTTPHandler) authorized(r *http.Request) bool {
	cookie, err := r.Cookie(sessionUICookieName)
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(h.config.Token)) == 1
}

func setSecurityHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; object-src 'none'; base-uri 'none'; frame-ancestors 'none'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "no-store")
}

func (h *SessionHTTPHandler) health(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, "ok\n")
}

func (h *SessionHTTPHandler) status(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	decision, err := h.coordinator.CurrentDecision(ctx)
	if err != nil {
		writeHTTPError(w, http.StatusServiceUnavailable, "routing status is unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"mode": decision.Mode, "observed_mode": decision.ObservedMode,
		"observed_at": decision.ObservedAt, "generation": decision.Generation,
		"fresh": decision.Fresh, "published": decision.Published,
		"can_start": decision.CanStart, "fallback_enabled": h.config.FallbackEnabled,
	})
}

func (h *SessionHTTPHandler) listSessions(w http.ResponseWriter, _ *http.Request) {
	type sessionSummary struct {
		ID       string    `json:"id"`
		Provider string    `json:"provider"`
		Model    string    `json:"model"`
		Effort   string    `json:"effort"`
		Mode     string    `json:"mode"`
		Created  time.Time `json:"created_at"`
	}
	sessions := h.sessions.List()
	result := make([]sessionSummary, 0, len(sessions))
	for _, session := range sessions {
		result = append(result, sessionSummary{ID: session.Thread.ID, Provider: session.Pin.Provider, Model: session.Pin.Model, Effort: session.Pin.Effort, Mode: session.Pin.Mode, Created: session.Pin.CreatedAt})
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": result})
}

func (h *SessionHTTPHandler) sessionHistory(w http.ResponseWriter, r *http.Request) {
	threadID := r.PathValue("id")
	if h.sessions.Get(threadID) == nil {
		writeHTTPError(w, http.StatusNotFound, "session not found")
		return
	}
	backend, ok := h.sessions.(SessionHistoryBackend)
	if !ok {
		writeHTTPError(w, http.StatusServiceUnavailable, "session history is unavailable")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	history, err := backend.History(ctx, threadID)
	if err != nil {
		writeHTTPError(w, http.StatusBadGateway, "Codex App Server history could not be loaded")
		return
	}
	writeJSON(w, http.StatusOK, history)
}

func (h *SessionHTTPHandler) createSession(w http.ResponseWriter, r *http.Request) {
	var input struct {
		CWD    string `json:"cwd"`
		Prompt string `json:"prompt"`
	}
	if err := decodeJSONBody(w, r, maxSessionBodyBytes, &input); err != nil {
		writeDecodeError(w, err)
		return
	}
	if len(input.Prompt) == 0 || len(input.Prompt) > maxTurnTextBytes {
		writeHTTPError(w, http.StatusBadRequest, "prompt must be between 1 byte and 64 KiB")
		return
	}
	cwd, err := validateWorkingDirectory(input.CWD)
	if err != nil {
		writeHTTPError(w, http.StatusBadRequest, "working directory must be an existing local directory")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	decision, err := h.coordinator.RefreshAndDecide(ctx)
	if err != nil {
		writeHTTPError(w, http.StatusServiceUnavailable, "fresh route decision is unavailable")
		return
	}
	if err := ctx.Err(); err != nil {
		writeHTTPError(w, http.StatusRequestTimeout, "session creation was cancelled before thread start")
		return
	}
	if !decision.CanStart || !decision.Published {
		writeHTTPError(w, http.StatusServiceUnavailable, "route publication is pending; no session was created")
		return
	}
	route, err := SessionRouteForMode(decision.Mode)
	if err != nil {
		writeHTTPError(w, http.StatusServiceUnavailable, "route mode is not supported")
		return
	}
	if decision.Mode == codexrouting.ModeQuotaFallback && !h.config.FallbackEnabled {
		writeHTTPError(w, http.StatusServiceUnavailable, "quota fallback remains disabled until subagent routing is runtime-verified")
		return
	}
	pin := RoutePin{Mode: string(decision.Mode), Provider: route.Provider, Model: route.Model, Effort: route.Effort, Generation: uint64(max(decision.Generation, 0)), CreatedAt: time.Now().UTC()}
	session, err := h.sessions.CreateThread(ctx, ThreadStartOptions{CWD: cwd, ModelProvider: route.Provider, Model: route.Model, Effort: route.Effort}, pin)
	if err != nil {
		writeHTTPError(w, http.StatusBadGateway, "Codex App Server could not create the pinned thread")
		return
	}
	if err := ctx.Err(); err != nil {
		writeJSON(w, http.StatusGatewayTimeout, map[string]any{"error": "thread was created but initial turn was cancelled; the thread was preserved", "session": publicSession(session), "turn_id": ""})
		return
	}
	turn, err := h.sessions.StartTurn(ctx, session.Thread.ID, input.Prompt)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": "initial turn failed; the thread was preserved and will not be replayed", "session": publicSession(session), "turn_id": ""})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"session": publicSession(session), "turn_id": turn.ID})
}

func publicSession(session *ManagedSession) map[string]any {
	return map[string]any{
		"id": session.Thread.ID, "provider": session.Pin.Provider,
		"model": session.Pin.Model, "effort": session.Pin.Effort,
		"mode": session.Pin.Mode, "generation": session.Pin.Generation,
		"created_at": session.Pin.CreatedAt,
	}
}

func validateWorkingDirectory(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" || !filepath.IsAbs(path) {
		return "", errors.New("working directory must be absolute")
	}
	clean, err := filepath.EvalSymlinks(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	info, err := os.Stat(clean)
	if err != nil || !info.IsDir() {
		return "", errors.New("working directory is not a directory")
	}
	return clean, nil
}

func (h *SessionHTTPHandler) startTurn(w http.ResponseWriter, r *http.Request) {
	threadID := r.PathValue("id")
	var input struct {
		Text string `json:"text"`
	}
	if err := decodeJSONBody(w, r, maxTurnTextBytes+1024, &input); err != nil {
		writeDecodeError(w, err)
		return
	}
	if strings.TrimSpace(input.Text) == "" || len(input.Text) > maxTurnTextBytes {
		writeHTTPError(w, http.StatusBadRequest, "turn text must be between 1 byte and 64 KiB")
		return
	}
	if h.sessions.Get(threadID) == nil {
		writeHTTPError(w, http.StatusNotFound, "session not found")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	turn, err := h.sessions.StartTurn(ctx, threadID, input.Text)
	if err != nil {
		writeHTTPError(w, http.StatusBadGateway, "Codex App Server could not start the turn; it was not retried")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"turn_id": turn.ID})
}

func (h *SessionHTTPHandler) interruptTurn(w http.ResponseWriter, r *http.Request) {
	threadID := r.PathValue("id")
	var input struct {
		TurnID string `json:"turn_id"`
	}
	if err := decodeJSONBody(w, r, 4096, &input); err != nil {
		writeDecodeError(w, err)
		return
	}
	if h.sessions.Get(threadID) == nil {
		writeHTTPError(w, http.StatusNotFound, "session not found")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := h.sessions.InterruptTurn(ctx, threadID, input.TurnID); err != nil {
		writeHTTPError(w, http.StatusBadGateway, "Codex App Server could not interrupt the turn")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *SessionHTTPHandler) respondApproval(w http.ResponseWriter, r *http.Request) {
	var input struct {
		ThreadID string `json:"thread_id"`
		ID       string `json:"id"`
		Decision string `json:"decision"`
	}
	if err := decodeJSONBody(w, r, 8192, &input); err != nil {
		writeDecodeError(w, err)
		return
	}
	if h.sessions.Get(input.ThreadID) == nil {
		writeHTTPError(w, http.StatusNotFound, "session not found")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := h.sessions.RespondApprovalID(ctx, input.ThreadID, input.ID, input.Decision); err != nil {
		writeHTTPError(w, http.StatusConflict, "approval is invalid, unsupported, or no longer pending")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *SessionHTTPHandler) streamSession(w http.ResponseWriter, r *http.Request) {
	session := h.sessions.Get(r.PathValue("id"))
	if session == nil {
		writeHTTPError(w, http.StatusNotFound, "session not found")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeHTTPError(w, http.StatusInternalServerError, "streaming is unavailable")
		return
	}
	h.sseMu.Lock()
	if h.sseActive >= h.config.MaxSSE || h.sseByThread[session.Thread.ID] > 0 {
		h.sseMu.Unlock()
		writeHTTPError(w, http.StatusTooManyRequests, "local event stream limit reached for this session")
		return
	}
	h.sseActive++
	h.sseByThread[session.Thread.ID]++
	h.sseMu.Unlock()
	defer func() {
		h.sseMu.Lock()
		h.sseActive--
		delete(h.sseByThread, session.Thread.ID)
		h.sseMu.Unlock()
	}()
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	for {
		select {
		case <-r.Context().Done():
			return
		case notice, ok := <-session.Notices:
			if !ok {
				return
			}
			visible, ok := publicSessionNotice(session.Thread.ID, notice)
			if !ok {
				continue
			}
			data, err := json.Marshal(visible)
			if err != nil {
				continue
			}
			if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func publicSessionNotice(threadID string, notice SessionNotice) (map[string]any, bool) {
	if notice.Approval != nil {
		return map[string]any{"type": "approval", "thread_id": notice.Approval.ThreadID, "turn_id": notice.Approval.TurnID, "approval": notice.Approval, "supported": notice.Supported, "error": notice.Error}, true
	}
	if notice.Error != "" {
		return map[string]any{"type": "stream_error", "message": notice.Error}, true
	}
	if notice.Event == nil {
		return nil, false
	}
	var params struct {
		ThreadID string `json:"threadId"`
		TurnID   string `json:"turnId"`
		ItemID   string `json:"itemId"`
		Delta    string `json:"delta"`
		Turn     struct {
			ID     string          `json:"id"`
			Status json.RawMessage `json:"status"`
		} `json:"turn"`
	}
	if json.Unmarshal(notice.Event.Params, &params) != nil || params.ThreadID != threadID {
		return nil, false
	}
	switch notice.Event.Method {
	case "item/agentMessage/delta":
		if params.TurnID == "" || params.ItemID == "" || len(params.Delta) > maxVisibleDelta {
			return nil, false
		}
		return map[string]any{"type": "assistant_delta", "thread_id": threadID, "turn_id": params.TurnID, "item_id": params.ItemID, "delta": params.Delta}, true
	case "turn/started", "turn/completed":
		turnID := params.Turn.ID
		if turnID == "" {
			turnID = params.TurnID
		}
		status := ""
		_ = json.Unmarshal(params.Turn.Status, &status)
		return map[string]any{"type": notice.Event.Method, "thread_id": threadID, "turn_id": turnID, "status": status}, true
	default:
		return nil, false
	}
}

func (h *SessionHTTPHandler) staticOrNotFound(w http.ResponseWriter, r *http.Request) {
	if (r.URL.Path == "/" || r.URL.Path == "/codex.html") && r.Method == http.MethodGet {
		http.SetCookie(w, &http.Cookie{Name: sessionUICookieName, Value: h.config.Token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
	}
	if h.static == nil || (r.Method != http.MethodGet && r.Method != http.MethodHead) {
		http.NotFound(w, r)
		return
	}
	h.static.ServeHTTP(w, r)
}

func decodeJSONBody(w http.ResponseWriter, r *http.Request, maxBytes int64, destination any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return errors.New("request body must contain exactly one JSON value")
	}
	return nil
}

func writeDecodeError(w http.ResponseWriter, err error) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeHTTPError(w, http.StatusRequestEntityTooLarge, "request body is too large")
		return
	}
	writeHTTPError(w, http.StatusBadRequest, "request body is invalid")
}

func writeHTTPError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
