package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/WilliamLi0623/codex-homelab/internal/domain"
	"github.com/WilliamLi0623/codex-homelab/internal/store"
)

type Server struct {
	store *store.Store
	mux   *http.ServeMux
}

type createTaskRequest struct {
	Repository     string `json:"repository"`
	BaseRef        string `json:"base_ref"`
	Objective      string `json:"objective"`
	Profile        string `json:"profile"`
	IdempotencyKey string `json:"idempotency_key"`
}

type taskResponse struct {
	ID             string `json:"id"`
	Repository     string `json:"repository"`
	BaseRef        string `json:"base_ref"`
	Objective      string `json:"objective"`
	ExecutionClass string `json:"execution_class"`
	State          string `json:"state"`
}

type createTaskResponse struct {
	Task taskResponse `json:"task"`
}

type listTasksResponse struct {
	Tasks []taskResponse `json:"tasks"`
}

type statusResponse struct {
	Controller string `json:"controller"`
	Tasks      int    `json:"tasks"`
}

type messageRequest struct {
	Body string `json:"body"`
}

type eventResponse struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	CreatedAt string `json:"created_at"`
}

type listEventsResponse struct {
	Events []eventResponse `json:"events"`
}

type messageResponse struct {
	ID        string `json:"id"`
	Role      string `json:"role"`
	Body      string `json:"body"`
	CreatedAt string `json:"created_at"`
}

type listMessagesResponse struct {
	Messages []messageResponse `json:"messages"`
}

type attemptResponse struct {
	ID           string `json:"id"`
	Number       int    `json:"number"`
	ModelProfile string `json:"model_profile"`
	State        string `json:"state"`
}

type retryTaskResponse struct {
	Task    taskResponse    `json:"task"`
	Attempt attemptResponse `json:"attempt"`
}

type reconcileAttemptRequest struct {
	Outcome string `json:"outcome"`
}

func NewServer(database *store.Store) *Server {
	server := &Server{store: database, mux: http.NewServeMux()}
	server.mux.HandleFunc("GET /v1/health", server.health)
	server.mux.HandleFunc("GET /v1/status", server.status)
	server.mux.HandleFunc("POST /v1/tasks", server.createTask)
	server.mux.HandleFunc("GET /v1/tasks", server.listTasks)
	server.mux.HandleFunc("GET /v1/tasks/{id}", server.getTask)
	server.mux.HandleFunc("POST /v1/tasks/{id}/messages", server.appendMessage)
	server.mux.HandleFunc("GET /v1/tasks/{id}/messages", server.listMessages)
	server.mux.HandleFunc("GET /v1/tasks/{id}/events", server.listEvents)
	server.mux.HandleFunc("POST /v1/tasks/{id}/cancel", server.cancelTask)
	server.mux.HandleFunc("POST /v1/tasks/{id}/retry", server.retryTask)
	server.mux.HandleFunc("POST /v1/tasks/{id}/attempts/{attemptID}/reconcile", server.reconcileAttempt)
	return server
}

func (s *Server) reconcileAttempt(writer http.ResponseWriter, request *http.Request) {
	var input reconcileAttemptRequest
	decoder := json.NewDecoder(http.MaxBytesReader(writer, request.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid reconciliation request"})
		return
	}
	outcome := domain.AttemptState(input.Outcome)
	if !isTerminalReconciliationOutcome(outcome) {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "outcome must be a known terminal attempt state"})
		return
	}
	attempt, err := s.store.ReconcileUnknownAttempt(request.Context(), request.PathValue("id"), request.PathValue("attemptID"), outcome)
	if errors.Is(err, store.ErrAttemptNotFound) {
		writeJSON(writer, http.StatusNotFound, map[string]string{"error": "attempt not found"})
		return
	}
	if err != nil {
		writeJSON(writer, http.StatusConflict, map[string]string{"error": "attempt cannot be reconciled"})
		return
	}
	writeJSON(writer, http.StatusOK, attemptResponse{ID: attempt.ID, Number: attempt.Number, ModelProfile: attempt.ModelProfile, State: string(attempt.State)})
}

func (s *Server) retryTask(writer http.ResponseWriter, request *http.Request) {
	task, attempt, err := s.store.RetryTask(request.Context(), request.PathValue("id"))
	if errors.Is(err, store.ErrTaskNotFound) {
		writeJSON(writer, http.StatusNotFound, map[string]string{"error": "task not found"})
		return
	}
	if err != nil {
		writeJSON(writer, http.StatusConflict, map[string]string{"error": "task cannot be retried"})
		return
	}
	writeJSON(writer, http.StatusCreated, retryTaskResponse{
		Task:    toTaskResponse(task),
		Attempt: attemptResponse{ID: attempt.ID, Number: attempt.Number, ModelProfile: attempt.ModelProfile, State: string(attempt.State)},
	})
}

func (s *Server) cancelTask(writer http.ResponseWriter, request *http.Request) {
	task, err := s.store.CancelTask(request.Context(), request.PathValue("id"))
	if errors.Is(err, store.ErrTaskNotFound) {
		writeJSON(writer, http.StatusNotFound, map[string]string{"error": "task not found"})
		return
	}
	if err != nil {
		writeJSON(writer, http.StatusConflict, map[string]string{"error": "task cannot be cancelled"})
		return
	}
	writeJSON(writer, http.StatusOK, createTaskResponse{Task: toTaskResponse(task)})
}

func (s *Server) appendMessage(writer http.ResponseWriter, request *http.Request) {
	var input messageRequest
	if err := json.NewDecoder(http.MaxBytesReader(writer, request.Body, 1<<20)).Decode(&input); err != nil || strings.TrimSpace(input.Body) == "" {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "message body is required"})
		return
	}
	message, err := s.store.AppendUserMessage(request.Context(), request.PathValue("id"), input.Body)
	if errors.Is(err, store.ErrTaskNotFound) {
		writeJSON(writer, http.StatusNotFound, map[string]string{"error": "task not found"})
		return
	}
	if err != nil {
		writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": "append task message failed"})
		return
	}
	writeJSON(writer, http.StatusCreated, map[string]string{"id": message.ID, "role": message.Role})
}

func (s *Server) listMessages(writer http.ResponseWriter, request *http.Request) {
	messages, err := s.store.ListTaskMessages(request.Context(), request.PathValue("id"))
	if errors.Is(err, store.ErrTaskNotFound) {
		writeJSON(writer, http.StatusNotFound, map[string]string{"error": "task not found"})
		return
	}
	if err != nil {
		writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": "list task messages failed"})
		return
	}
	response := listMessagesResponse{Messages: make([]messageResponse, 0, len(messages))}
	for _, message := range messages {
		response.Messages = append(response.Messages, messageResponse{ID: message.ID, Role: message.Role, Body: message.Body, CreatedAt: message.CreatedAt.Format(time.RFC3339Nano)})
	}
	writeJSON(writer, http.StatusOK, response)
}

func (s *Server) listEvents(writer http.ResponseWriter, request *http.Request) {
	events, err := s.store.ListTaskEvents(request.Context(), request.PathValue("id"))
	if errors.Is(err, store.ErrTaskNotFound) {
		writeJSON(writer, http.StatusNotFound, map[string]string{"error": "task not found"})
		return
	}
	if err != nil {
		writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": "list task events failed"})
		return
	}
	response := listEventsResponse{Events: make([]eventResponse, 0, len(events))}
	for _, event := range events {
		response.Events = append(response.Events, eventResponse{ID: event.ID, Type: event.Type, CreatedAt: event.CreatedAt.Format(time.RFC3339Nano)})
	}
	writeJSON(writer, http.StatusOK, response)
}

func (s *Server) listTasks(writer http.ResponseWriter, request *http.Request) {
	tasks, err := s.store.ListTasks(request.Context())
	if err != nil {
		writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": "list tasks failed"})
		return
	}
	response := listTasksResponse{Tasks: make([]taskResponse, 0, len(tasks))}
	for _, task := range tasks {
		response.Tasks = append(response.Tasks, toTaskResponse(task))
	}
	writeJSON(writer, http.StatusOK, response)
}

func (s *Server) getTask(writer http.ResponseWriter, request *http.Request) {
	task, err := s.store.GetTask(request.Context(), request.PathValue("id"))
	if errors.Is(err, store.ErrTaskNotFound) {
		writeJSON(writer, http.StatusNotFound, map[string]string{"error": "task not found"})
		return
	}
	if err != nil {
		writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": "get task failed"})
		return
	}
	writeJSON(writer, http.StatusOK, createTaskResponse{Task: toTaskResponse(task)})
}

func (s *Server) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	s.mux.ServeHTTP(writer, request)
}

func (s *Server) health(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) status(writer http.ResponseWriter, request *http.Request) {
	tasks, err := s.store.ListTasks(request.Context())
	if err != nil {
		writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": "read controller status failed"})
		return
	}
	writeJSON(writer, http.StatusOK, statusResponse{Controller: "ok", Tasks: len(tasks)})
}

func (s *Server) createTask(writer http.ResponseWriter, request *http.Request) {
	var input createTaskRequest
	decoder := json.NewDecoder(http.MaxBytesReader(writer, request.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid task request"})
		return
	}
	if strings.TrimSpace(input.Repository) == "" || strings.TrimSpace(input.BaseRef) == "" || strings.TrimSpace(input.Objective) == "" || strings.TrimSpace(input.IdempotencyKey) == "" {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "repository, base_ref, objective, and idempotency_key are required"})
		return
	}

	task := domain.NewTask(newTaskID(), input.Repository, input.BaseRef, input.Objective, input.IdempotencyKey)
	persisted, created, err := s.store.CreateTask(request.Context(), task)
	if errors.Is(err, store.ErrIdempotencyConflict) {
		writeJSON(writer, http.StatusConflict, map[string]string{"error": "idempotency key belongs to a different request"})
		return
	}
	if err != nil {
		writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": "create task failed"})
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(writer, status, createTaskResponse{Task: toTaskResponse(persisted)})
}

func toTaskResponse(task domain.Task) taskResponse {
	return taskResponse{
		ID:             task.ID,
		Repository:     task.Repository,
		BaseRef:        task.BaseRef,
		Objective:      task.Objective,
		ExecutionClass: string(task.ExecutionClass),
		State:          string(task.State),
	}
}

func isTerminalReconciliationOutcome(state domain.AttemptState) bool {
	return state == domain.AttemptProviderFailed ||
		state == domain.AttemptExecutionFailed ||
		state == domain.AttemptValidationFailed ||
		state == domain.AttemptCompleted ||
		state == domain.AttemptCancelled
}

func newTaskID() string {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	return "task-" + hex.EncodeToString(bytes)
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
