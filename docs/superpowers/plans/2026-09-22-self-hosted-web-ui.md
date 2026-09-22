# Self-Hosted Web UI Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Provide a private task console that uses the Controller APIs for task creation, execution, events, conversation, artifacts, and guarded recovery.

**Architecture:** A TypeScript/React frontend talks to a same-origin BFF. The BFF authenticates the operator and calls the Controller with a service credential; the browser never calls Proxmox or holds Controller secrets.

**Tech Stack:** TypeScript, React, Vite, browser EventSource, Go BFF, existing Controller REST API.

**Spec:** `docs/superpowers/specs/2026-09-22-dual-interface-design.md`

## Global Constraints

- The UI is private-network/Tailscale-only until authentication and authorization are verified.
- The BFF allowlists Controller paths and redacts secrets/errors.
- UI actions use idempotency keys and display UNKNOWN instead of replaying actions.
- No hidden reasoning, raw credentials, or unrestricted tool output is rendered.
- The UI does not implement task scheduling, VMID allocation, or provider routing.

### Task 1: Frontend shell and typed API client

**Files:**
- Create: `ui/package.json`
- Create: `ui/tsconfig.json`
- Create: `ui/vite.config.ts`
- Create: `ui/src/api/controller.ts`
- Create: `ui/src/types.ts`
- Create: `ui/src/App.tsx`
- Test: `ui/src/api/controller.test.ts`

- [ ] Write client tests for task list, task detail, event cursor, and redacted error responses.
- [ ] Run `npm test -- --run` in `ui` and confirm failure before implementation.
- [ ] Implement typed fetch helpers with same-origin credentials and no embedded service token.
- [ ] Implement the shell with route placeholders and explicit loading/error/UNKNOWN states.
- [ ] Run `npm test -- --run` and `npm run build`.
- [ ] Commit `feat: add typed private task ui shell`.

### Task 2: BFF and event stream

**Files:**
- Create: `cmd/task-ui-bff/main.go`
- Create: `cmd/task-ui-bff/main_test.go`
- Create: `ui/src/api/events.ts`
- Create: `ui/src/api/events.test.ts`

- [ ] Write BFF tests proving allowlisted paths, request-size limits, auth rejection, and Controller error redaction.
- [ ] Write EventSource tests for cursor resume, duplicate suppression, terminal close, and reconnect backoff.
- [ ] Run targeted Go and frontend tests and confirm failure.
- [ ] Implement the BFF proxy and event stream adapter without exposing Controller credentials.
- [ ] Run `go test ./cmd/task-ui-bff -count=1`, `npm test -- --run`, and `npm run build`.
- [ ] Commit `feat: add private task ui backend and event stream`.

### Task 3: Task and conversation views

**Files:**
- Create: `ui/src/pages/Dashboard.tsx`
- Create: `ui/src/pages/NewTask.tsx`
- Create: `ui/src/pages/TaskDetail.tsx`
- Create: `ui/src/components/EventTimeline.tsx`
- Create: `ui/src/components/ChatPanel.tsx`
- Create: `ui/src/components/ToolCallCard.tsx`
- Test: `ui/src/pages/TaskDetail.test.tsx`

- [ ] Write component tests for create/dispatch, event rendering, follow-up conflict, and UNKNOWN display.
- [ ] Implement Dashboard and New Task against the typed BFF client.
- [ ] Implement Task Detail with SSE event updates and polling fallback.
- [ ] Render only sanitized tool summaries and bounded outputs.
- [ ] Implement follow-up through the Controller continuation endpoint; disable input when no active attempt exists.
- [ ] Run frontend tests and build.
- [ ] Commit `feat: add task detail and continuation views`.

### Task 4: Guarded operations and deployment

**Files:**
- Create: `ui/src/pages/Operations.tsx`
- Create: `ui/src/components/ConfirmAction.tsx`
- Create: `deploy/systemd/codex-task-ui-bff.service`
- Create: `docs/integrations/self-hosted-ui.md`
- Modify: `docs/security.md`

- [ ] Write tests for cancel/retry/reconcile confirmation and idempotency-key reuse.
- [ ] Implement admin-only operations with explicit impact text and no destructive PVE command exposure.
- [ ] Add a systemd unit that binds the BFF privately and runs as a restricted user.
- [ ] Document Tailscale/private HTTPS deployment and rollback by stopping only the new UI services.
- [ ] Run Go tests, frontend tests, build, and a browser smoke test against a fake Controller.
- [ ] Commit `feat: add guarded task ui operations`.

### Task 5: Web UI acceptance

- [ ] Create a task from the UI and verify it reaches the existing Controller API.
- [ ] Verify the same task is visible through `codexctl` and MCP.
- [ ] Verify event reconnect after a BFF restart does not duplicate events.
- [ ] Verify a continuation reaches the same attempt or returns an explicit conflict/UNKNOWN result.
- [ ] Verify no service token, provider key, Proxmox token, or raw hidden reasoning appears in browser network responses or logs.
- [ ] Record evidence in `docs/migration/p27-dual-interface-2026-09-22.md`.
- [ ] Commit `docs: record self-hosted web ui acceptance`.
