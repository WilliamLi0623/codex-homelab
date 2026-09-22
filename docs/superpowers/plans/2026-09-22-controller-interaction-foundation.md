# Controller Interaction Foundation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add cursor-based event retrieval, SSE delivery, and durable active-worker continuation to the existing V3 Controller without changing scheduling ownership.

**Architecture:** Extend the existing API/store/executor seams. Persist every continuation request before delivery, use the persisted execution handle to address the existing executor `SendMessage` path, and fail closed into UNKNOWN when delivery is ambiguous.

**Tech Stack:** Go 1.22, `net/http`, existing SQLite store, existing K3s executor, existing Controller API tests.

**Spec:** `docs/superpowers/specs/2026-09-22-dual-interface-design.md`

## Global Constraints

- Do not restore WebCodex, `cloudflared`, or VM101 runner services.
- Do not modify OpenAI provider behavior or dynamic VMID ownership.
- Do not automatically retry an ambiguous worker message.
- Do not expose raw prompts, secrets, hidden reasoning, or unrestricted tool output in events.
- Preserve idempotency, UNKNOWN, execution-handle, and release invariants.

### Task 1: Event cursor contract

**Files:**
- Modify: `internal/store/events.go`
- Modify: `internal/api/server.go`
- Create: `internal/api/events_stream.go`
- Test: `internal/store/events_test.go`
- Test: `internal/api/events_stream_test.go`

**Interfaces:**
- Produce `ListTaskEventsAfter(ctx, taskID, afterID string) ([]TaskEvent, error)`.
- Produce `GET /v1/tasks/{id}/events?after=<event_id>` with stable ordering.
- Produce `GET /v1/tasks/{id}/events/stream` using `Last-Event-ID` and SSE framing.

- [ ] Write tests for empty cursor, known cursor, unknown cursor, task-not-found, and stable event order.
- [ ] Run `go test ./internal/store ./internal/api -run 'Test.*Event' -count=1` and confirm the new tests fail before implementation.
- [ ] Implement the bounded query and HTTP handlers without returning event payloads that violate the existing sanitization boundary.
- [ ] Add SSE flushing, `id`, `event`, and `data` fields plus terminal stream closure.
- [ ] Run the targeted tests and `go test ./internal/store ./internal/api -count=1`.
- [ ] Commit `feat: add cursorable controller event stream`.

### Task 2: Durable continuation delivery

**Files:**
- Modify: `internal/store/events.go`
- Create: `internal/store/continuations.go`
- Create: `internal/store/continuations_test.go`
- Modify: `internal/executor/k3s/executor.go`
- Modify: `internal/api/server.go`
- Create: `internal/api/continuation_test.go`

**Interfaces:**
- Add a persisted delivery record keyed by `(task_id, attempt_id, idempotency_key)` with `PENDING`, `DELIVERED`, and `UNKNOWN` states.
- Add `POST /v1/tasks/{id}/turns` accepting `{attempt_id, body, idempotency_key}`.
- Reuse the existing executor send seam after resolving the persisted execution handle.

- [ ] Write store tests proving duplicate idempotency keys return the original delivery and never append a second user message.
- [ ] Write API tests proving missing active handles return conflict and ambiguous executor errors become UNKNOWN.
- [ ] Run `go test ./internal/store ./internal/api ./internal/executor/k3s -run 'Test.*Continuation|Test.*Turn|Test.*Send' -count=1` and confirm the new tests fail.
- [ ] Implement the smallest schema/migration needed for delivery state and bounded error class.
- [ ] Resolve execution handles through the existing store contract; do not reconstruct a Job from task text or create a replacement claim.
- [ ] Call executor send only for a newly claimed PENDING record; map confirmed pre-send failures to a safe failure and ambiguous failures to UNKNOWN.
- [ ] Emit durable events for accepted, delivered, and UNKNOWN outcomes.
- [ ] Run focused tests followed by `go test ./internal/store ./internal/api ./internal/executor/k3s -count=1`.
- [ ] Commit `feat: add durable task continuation delivery`.

### Task 3: Controller integration and crash verification

**Files:**
- Modify: `cmd/controller/main.go`
- Modify: `cmd/controller/main_test.go`
- Modify: `docs/operations.md`
- Modify: `docs/recovery.md`

**Interfaces:**
- Production assembly exposes the event stream and continuation endpoint through the existing Controller listener.
- Restart recovery observes PENDING/UNKNOWN delivery records without replaying ambiguous requests.

- [ ] Add integration tests that assemble the production handler with fake Proxmox/K3s seams and verify readiness remains fail-closed when dependencies are absent.
- [ ] Add a restart-shaped store test showing a PENDING record is reconciled explicitly rather than sent automatically.
- [ ] Run `go test ./cmd/controller ./internal/api ./internal/store -count=1`.
- [ ] Update operations docs with curl/PowerShell examples and explicit UNKNOWN handling.
- [ ] Commit `test: verify controller continuation recovery boundaries`.

### Task 4: Foundation acceptance

- [ ] Run `go test ./... -count=1` with repository-local caches.
- [ ] Run a non-destructive `httptest` scenario: create task, start attempt, record execution handle, dispatch fake job, send one continuation, repeat the same idempotency key, and confirm one delivery.
- [ ] Run a failure scenario where the fake executor returns an ambiguous error and confirm no second send occurs after reopening the store.
- [ ] Record evidence in `docs/migration/p27-dual-interface-2026-09-22.md`.
- [ ] Commit `docs: record dual-interface foundation evidence`.
