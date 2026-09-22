package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/WilliamLi0623/codex-homelab/internal/domain"
	"github.com/WilliamLi0623/codex-homelab/internal/orchestrator"
	"github.com/WilliamLi0623/codex-homelab/internal/store"
)

const workerCommitBoundary = "\n\nExecution contract: use the terminal tool to perform and verify the requested changes. Do not run git commit or git push; the worker wrapper validates the workspace and creates the local commit."

type Dispatcher interface {
	Dispatch(context.Context, orchestrator.Request) (orchestrator.Dispatch, error)
}

type Completer interface {
	Complete(context.Context, orchestrator.CompletionInput) (store.CompletionRecord, error)
}

type ReleaseReconciler interface {
	ReconcileRelease(context.Context, orchestrator.Claim, string) error
}

type AttemptMessageSender interface {
	SendMessageForAttempt(context.Context, string, string) error
}

type dispatchRequest struct {
	AttemptID         string   `json:"attempt_id"`
	Prompt            string   `json:"prompt"`
	ValidationCommand []string `json:"validation_command,omitempty"`
}

type completeRequest struct {
	Branch            string   `json:"branch"`
	ValidationCommand []string `json:"validation_command"`
}

type dispatchResponse struct {
	TaskID    string `json:"task_id"`
	AttemptID string `json:"attempt_id"`
	ClaimID   string `json:"claim_id"`
	VMID      int    `json:"vmid"`
	JobID     string `json:"job_id"`
	State     string `json:"state"`
}
type Server struct {
	store         *store.Store
	dispatcher    Dispatcher
	completer     Completer
	releaser      ReleaseReconciler
	messageSender AttemptMessageSender
	mux           *http.ServeMux
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

type continuationRequest struct {
	AttemptID      string `json:"attempt_id"`
	Body           string `json:"body"`
	IdempotencyKey string `json:"idempotency_key"`
}

type continuationResponse struct {
	ID             string `json:"id"`
	TaskID         string `json:"task_id"`
	AttemptID      string `json:"attempt_id"`
	IdempotencyKey string `json:"idempotency_key"`
	State          string `json:"state"`
	ErrorSummary   string `json:"error_summary,omitempty"`
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

type reconcileReleaseRequest struct {
	VMID       int    `json:"vmid"`
	Generation string `json:"generation"`
	KubeNode   string `json:"kube_node"`
	Proof      string `json:"proof"`
}

type startAttemptRequest struct {
	Profile string `json:"profile"`
}

func NewServer(database *store.Store) *Server {
	return NewServerWithDispatcher(database, nil)
}

func NewServerWithDispatcher(database *store.Store, dispatcher Dispatcher) *Server {
	return NewServerWithDispatcherAndCompletion(database, dispatcher, nil)
}

func NewServerWithDispatcherAndCompletion(database *store.Store, dispatcher Dispatcher, completer Completer) *Server {
	return NewServerWithDispatcherCompletionAndRelease(database, dispatcher, completer, nil)
}

func NewServerWithDispatcherCompletionAndRelease(database *store.Store, dispatcher Dispatcher, completer Completer, releaser ReleaseReconciler) *Server {
	return NewServerWithDispatcherCompletionReleaseAndMessageSender(database, dispatcher, completer, releaser, nil)
}

func NewServerWithDispatcherCompletionReleaseAndMessageSender(database *store.Store, dispatcher Dispatcher, completer Completer, releaser ReleaseReconciler, messageSender AttemptMessageSender) *Server {
	server := &Server{store: database, dispatcher: dispatcher, completer: completer, releaser: releaser, messageSender: messageSender, mux: http.NewServeMux()}
	server.mux.HandleFunc("GET /v1/health", server.health)
	server.mux.HandleFunc("GET /v1/ready", server.ready)
	server.mux.HandleFunc("GET /v1/status", server.status)
	server.mux.HandleFunc("POST /v1/tasks", server.createTask)
	server.mux.HandleFunc("GET /v1/tasks", server.listTasks)
	server.mux.HandleFunc("GET /v1/tasks/{id}", server.getTask)
	server.mux.HandleFunc("POST /v1/tasks/{id}/messages", server.appendMessage)
	server.mux.HandleFunc("POST /v1/tasks/{id}/turns", server.continueTask)
	server.mux.HandleFunc("GET /v1/tasks/{id}/messages", server.listMessages)
	server.mux.HandleFunc("GET /v1/tasks/{id}/events", server.listEvents)
	server.mux.HandleFunc("GET /v1/tasks/{id}/events/stream", server.streamEvents)
	server.mux.HandleFunc("POST /v1/tasks/{id}/cancel", server.cancelTask)
	server.mux.HandleFunc("POST /v1/tasks/{id}/attempts", server.startAttempt)
	server.mux.HandleFunc("POST /v1/tasks/{id}/dispatch", server.dispatchTask)
	server.mux.HandleFunc("POST /v1/tasks/{id}/attempts/{attemptID}/complete", server.completeAttempt)
	server.mux.HandleFunc("POST /v1/tasks/{id}/retry", server.retryTask)
	server.mux.HandleFunc("POST /v1/tasks/{id}/attempts/{attemptID}/reconcile", server.reconcileAttempt)
	server.mux.HandleFunc("POST /v1/tasks/{id}/attempts/{attemptID}/release/reconcile", server.reconcileRelease)
	return server
}

func (s *Server) completeAttempt(writer http.ResponseWriter, request *http.Request) {
	if s.completer == nil {
		writeJSON(writer, http.StatusServiceUnavailable, map[string]string{"error": "completion consumer is not configured"})
		return
	}
	var input completeRequest
	decoder := json.NewDecoder(http.MaxBytesReader(writer, request.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil || len(input.ValidationCommand) == 0 {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "validation_command is required"})
		return
	}
	taskID := request.PathValue("id")
	attemptID := request.PathValue("attemptID")
	deterministicBranch := deterministicAttemptBranch(taskID, attemptID)
	if supplied := strings.TrimSpace(input.Branch); supplied != "" && supplied != deterministicBranch {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "branch must match the deterministic attempt branch"})
		return
	}
	record, err := s.completer.Complete(request.Context(), orchestrator.CompletionInput{TaskID: taskID, AttemptID: attemptID, Branch: deterministicBranch, ValidationCommand: input.ValidationCommand})
	if errors.Is(err, orchestrator.ErrCompletionReleasePending) {
		writeJSON(writer, http.StatusAccepted, map[string]any{"completion": record, "release_pending": true})
		return
	}
	if err != nil {
		writeJSON(writer, http.StatusConflict, map[string]string{"error": "attempt completion failed"})
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"completion": record})
}

func (s *Server) dispatchTask(writer http.ResponseWriter, request *http.Request) {
	if s.dispatcher == nil {
		writeJSON(writer, http.StatusServiceUnavailable, map[string]string{"error": "dispatcher is not configured"})
		return
	}
	var input dispatchRequest
	decoder := json.NewDecoder(http.MaxBytesReader(writer, request.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil || strings.TrimSpace(input.AttemptID) == "" || strings.TrimSpace(input.Prompt) == "" || len(input.ValidationCommand) == 0 {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "attempt_id, prompt, and validation_command are required"})
		return
	}
	task, err := s.store.GetTask(request.Context(), request.PathValue("id"))
	if errors.Is(err, store.ErrTaskNotFound) {
		writeJSON(writer, http.StatusNotFound, map[string]string{"error": "task not found"})
		return
	}
	if err != nil {
		writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": "load task failed"})
		return
	}
	attempt, err := s.store.GetAttempt(request.Context(), task.ID, input.AttemptID)
	if errors.Is(err, store.ErrAttemptNotFound) {
		writeJSON(writer, http.StatusNotFound, map[string]string{"error": "attempt not found for task"})
		return
	} else if err != nil {
		writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": "load attempt failed"})
		return
	}
	if _, _, err := s.store.EnsureAttemptExecutionSpec(request.Context(), store.AttemptExecutionSpec{
		TaskID: task.ID, AttemptID: input.AttemptID,
		Branch:            deterministicAttemptBranch(task.ID, input.AttemptID),
		ValidationCommand: input.ValidationCommand,
	}); err != nil {
		if errors.Is(err, store.ErrExecutionSpecConflict) {
			writeJSON(writer, http.StatusConflict, map[string]string{"error": "attempt execution spec conflicts with prior dispatch"})
			return
		}
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid execution spec"})
		return
	}
	dispatch, err := s.dispatcher.Dispatch(request.Context(), orchestrator.Request{TaskID: task.ID, AttemptID: input.AttemptID, ModelProfile: attempt.ModelProfile, Prompt: strings.TrimSpace(input.Prompt) + workerCommitBoundary, Repository: task.Repository, BaseRef: task.BaseRef, WorkspacePath: "/workspace/" + input.AttemptID, ValidationCommand: input.ValidationCommand})
	if err != nil {
		log.Printf("task dispatch failed task=%s attempt=%s: %v", task.ID, input.AttemptID, err)
		writeJSON(writer, http.StatusConflict, map[string]string{"error": "task dispatch failed"})
		return
	}
	writeJSON(writer, http.StatusAccepted, dispatchResponse{TaskID: task.ID, AttemptID: input.AttemptID, ClaimID: dispatch.Claim.ID, VMID: dispatch.Claim.VMID, JobID: dispatch.Job.ID, State: string(dispatch.Job.State)})
}

func deterministicAttemptBranch(taskID, attemptID string) string {
	return "refs/heads/codex/" + taskID + "/" + attemptID
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

func (s *Server) reconcileRelease(writer http.ResponseWriter, request *http.Request) {
	if s.releaser == nil {
		writeJSON(writer, http.StatusServiceUnavailable, map[string]string{"error": "release reconciler is not configured"})
		return
	}
	var input reconcileReleaseRequest
	decoder := json.NewDecoder(http.MaxBytesReader(writer, request.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil || input.VMID < 3000 || input.VMID > 3999 || strings.TrimSpace(input.Generation) == "" || strings.TrimSpace(input.KubeNode) == "" || strings.TrimSpace(input.Proof) == "" || len(input.Proof) > 2048 {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "vmid, generation, kube_node, and proof are required and valid"})
		return
	}
	taskID := request.PathValue("id")
	attemptID := request.PathValue("attemptID")
	stored, err := s.store.GetCapacityClaim(request.Context(), taskID, attemptID)
	if errors.Is(err, store.ErrCapacityClaimNotFound) {
		writeJSON(writer, http.StatusNotFound, map[string]string{"error": "capacity claim not found"})
		return
	}
	if err != nil {
		writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": "load capacity claim failed"})
		return
	}
	if stored.VMID != input.VMID || stored.Generation != input.Generation {
		writeJSON(writer, http.StatusConflict, map[string]string{"error": "release identity does not match durable capacity claim"})
		return
	}
	progress, err := s.store.GetReleaseProgress(request.Context(), taskID, attemptID)
	if errors.Is(err, store.ErrReleaseProgressNotFound) {
		writeJSON(writer, http.StatusConflict, map[string]string{"error": "release progress is not ready for reconciliation"})
		return
	}
	if err != nil {
		writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": "load release progress failed"})
		return
	}
	if progress.State != store.ReleaseStateUnknown || progress.VMID != input.VMID || progress.Generation != input.Generation || progress.KubeNode != input.KubeNode {
		writeJSON(writer, http.StatusConflict, map[string]string{"error": "release identity or state does not match durable release progress"})
		return
	}
	claim := orchestrator.Claim{ID: stored.ID, VMID: stored.VMID, TaskID: stored.TaskID, AttemptID: stored.AttemptID, Generation: stored.Generation, KubeNode: progress.KubeNode}
	if err := s.releaser.ReconcileRelease(request.Context(), claim, strings.TrimSpace(input.Proof)); err != nil {
		writeJSON(writer, http.StatusConflict, map[string]string{"error": "release cannot be reconciled"})
		return
	}
	writeJSON(writer, http.StatusOK, map[string]string{"status": "reconciled"})
}

func (s *Server) startAttempt(writer http.ResponseWriter, request *http.Request) {
	var input startAttemptRequest
	if request.Body != nil {
		decoder := json.NewDecoder(http.MaxBytesReader(writer, request.Body, 1<<20))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil && !errors.Is(err, io.EOF) {
			writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid attempt request"})
			return
		}
	}
	if input.Profile == "" {
		input.Profile = "openai-primary"
	}
	task, attempt, err := s.store.StartAttempt(request.Context(), request.PathValue("id"), input.Profile)
	if errors.Is(err, store.ErrTaskNotFound) {
		writeJSON(writer, http.StatusNotFound, map[string]string{"error": "task not found"})
		return
	}
	if err != nil {
		writeJSON(writer, http.StatusConflict, map[string]string{"error": "attempt cannot be started"})
		return
	}
	writeJSON(writer, http.StatusCreated, retryTaskResponse{Task: toTaskResponse(task), Attempt: attemptResponse{ID: attempt.ID, Number: attempt.Number, ModelProfile: attempt.ModelProfile, State: string(attempt.State)}})
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

func (s *Server) continueTask(writer http.ResponseWriter, request *http.Request) {
	if s.messageSender == nil {
		writeJSON(writer, http.StatusServiceUnavailable, map[string]string{"error": "attempt message sender is not configured"})
		return
	}
	var input continuationRequest
	decoder := json.NewDecoder(http.MaxBytesReader(writer, request.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil || strings.TrimSpace(input.AttemptID) == "" || strings.TrimSpace(input.Body) == "" || strings.TrimSpace(input.IdempotencyKey) == "" {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "attempt_id, body, and idempotency_key are required"})
		return
	}

	continuation, created, err := s.store.DeliverContinuation(request.Context(), request.PathValue("id"), strings.TrimSpace(input.AttemptID), strings.TrimSpace(input.IdempotencyKey), input.Body, s.messageSender.SendMessageForAttempt)
	if errors.Is(err, store.ErrTaskNotFound) {
		writeJSON(writer, http.StatusNotFound, map[string]string{"error": "task not found"})
		return
	}
	if errors.Is(err, store.ErrAttemptNotFound) {
		writeJSON(writer, http.StatusNotFound, map[string]string{"error": "attempt not found for task"})
		return
	}
	if errors.Is(err, store.ErrContinuationConflict) {
		writeJSON(writer, http.StatusConflict, map[string]string{"error": "idempotency key conflicts with prior continuation"})
		return
	}
	if !created && err == nil {
		writeJSON(writer, http.StatusAccepted, toContinuationResponse(continuation))
		return
	}
	if err != nil {
		if errors.Is(err, store.ErrExecutionHandleNotFound) {
			writeJSON(writer, http.StatusConflict, toContinuationResponse(continuation))
			return
		}
		if errors.Is(err, store.ErrContinuationDeliveryUnknown) {
			writeJSON(writer, http.StatusAccepted, toContinuationResponse(continuation))
			return
		}
		writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": "persist continuation delivery failed"})
		return
	}
	writeJSON(writer, http.StatusAccepted, toContinuationResponse(continuation))
}

func toContinuationResponse(continuation store.TaskContinuation) continuationResponse {
	return continuationResponse{
		ID:             continuation.ID,
		TaskID:         continuation.TaskID,
		AttemptID:      continuation.AttemptID,
		IdempotencyKey: continuation.IdempotencyKey,
		State:          continuation.State,
		ErrorSummary:   continuation.ErrorSummary,
	}
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
	events, err := s.store.ListTaskEventsAfter(request.Context(), request.PathValue("id"), request.URL.Query().Get("after"))
	if errors.Is(err, store.ErrTaskNotFound) {
		writeJSON(writer, http.StatusNotFound, map[string]string{"error": "task not found"})
		return
	}
	if errors.Is(err, store.ErrTaskEventCursorNotFound) {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "event cursor not found"})
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

func (s *Server) streamEvents(writer http.ResponseWriter, request *http.Request) {
	if _, err := s.store.GetTask(request.Context(), request.PathValue("id")); errors.Is(err, store.ErrTaskNotFound) {
		writeJSON(writer, http.StatusNotFound, map[string]string{"error": "task not found"})
		return
	} else if err != nil {
		writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": "load task failed"})
		return
	}
	writer.Header().Set("Content-Type", "text/event-stream")
	writer.Header().Set("Cache-Control", "no-cache")
	writer.Header().Set("Connection", "keep-alive")
	flusher, ok := writer.(http.Flusher)
	if !ok {
		writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": "event stream is not supported"})
		return
	}

	after := request.URL.Query().Get("after")
	if after == "" {
		after = request.Header.Get("Last-Event-ID")
	}
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		events, err := s.store.ListTaskEventsAfter(request.Context(), request.PathValue("id"), after)
		if errors.Is(err, store.ErrTaskEventCursorNotFound) {
			writeSSEError(writer, "event cursor not found")
			return
		}
		if err != nil {
			writeSSEError(writer, "list task events failed")
			return
		}
		for _, event := range events {
			if err := writeSSEEvent(writer, eventResponse{ID: event.ID, Type: event.Type, CreatedAt: event.CreatedAt.Format(time.RFC3339Nano)}); err != nil {
				return
			}
			after = event.ID
			if isTerminalEvent(event.Type) {
				return
			}
		}
		select {
		case <-request.Context().Done():
			return
		case <-ticker.C:
		}
		flusher.Flush()
	}
}

func writeSSEEvent(writer http.ResponseWriter, event eventResponse) error {
	encoded, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(writer, "id: %s\nevent: %s\ndata: %s\n\n", event.ID, event.Type, encoded); err != nil {
		return err
	}
	if flusher, ok := writer.(http.Flusher); ok {
		flusher.Flush()
	}
	return nil
}

func writeSSEError(writer http.ResponseWriter, message string) {
	_ = writeSSEEvent(writer, eventResponse{Type: "controller.error", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)})
	log.Printf("event stream error: %s", message)
}

func isTerminalEvent(eventType string) bool {
	switch eventType {
	case "task.succeeded", "task.failed", "task.blocked", "task.cancelled", "attempt.COMPLETED", "attempt.PROVIDER_FAILED", "attempt.EXECUTION_FAILED", "attempt.VALIDATION_FAILED", "attempt.CANCELLED":
		return true
	default:
		return false
	}
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

func (s *Server) ready(writer http.ResponseWriter, _ *http.Request) {
	if s.dispatcher == nil {
		writeJSON(writer, http.StatusServiceUnavailable, map[string]string{"error": "dispatcher is not configured"})
		return
	}
	writeJSON(writer, http.StatusOK, map[string]string{"status": "ready"})
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
