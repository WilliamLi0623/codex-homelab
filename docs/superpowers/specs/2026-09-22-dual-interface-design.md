# V3 dual-interface task interaction design

Status: approved design baseline for implementation. This document does not
restore the frozen V2 WebCodex services or change the Proxmox/K3s ownership
boundaries.

## Goal

Provide two interchangeable user interfaces for the existing V3 Controller:

1. a ChatGPT web custom MCP app for natural-language task interaction; and
2. a private self-hosted web console for complete task, event, artifact, and
   recovery visibility.

Both interfaces use the same Controller state and execution path:

```text
ChatGPT MCP App or self-hosted Web UI
              -> MCP/API gateway
              -> LXC210 Controller
              -> model router and execution broker
              -> Proxmox -> K3s -> dynamic LXC -> Codex
              -> validation -> commit/publication -> cleanup
```

## Current evidence and constraints

- LXC210 `codex-control` is the authoritative task API, MCP surface, SQLite
  store, router, broker, and capacity-manager boundary.
- The existing `internal/mcp` package is transport-neutral and already exposes
  task lifecycle tools, but it is not yet a remote MCP HTTP service.
- The existing task API persists messages, but `send_message` is not yet a
  reliable continuation request to an active worker.
- Existing worker continuation is available below the Controller through the
  executor seam; the durable Controller must own delivery and reconciliation.
- Observable task events may expose bounded command/result summaries, but never
  hidden reasoning, credentials, or unrestricted tool output.
- VM101 and the old LXC210 WebCodex/cloudflared chain remain diagnostic/recovery
  boundaries only. No old runner is restored.
- Controller, Proxmox, K3s, worker images, provider keys, and task isolation
  remain authoritative and are not duplicated in either UI.

## Architecture

### Shared Controller interaction layer

The Controller remains the only component allowed to create/release dynamic
VMIDs, dispatch K3s Jobs, select persisted model profiles, validate commits,
publish results, and reconcile UNKNOWN outcomes.

The first new interface is a durable continuation contract:

```text
POST /v1/tasks/{task_id}/turns
  {attempt_id, body, idempotency_key}
       -> append message
       -> resolve persisted execution handle
       -> send exactly once or enter UNKNOWN
       -> stream resulting events
```

The existing append-only message endpoint remains available for audit and
offline follow-ups. It must not be presented as a live TTY.

Task events gain cursor-based retrieval and an SSE stream:

```text
GET /v1/tasks/{task_id}/events?after=<event_id>
GET /v1/tasks/{task_id}/events/stream
```

Reconnection uses `Last-Event-ID`; duplicate events are harmless to clients.
Payloads are sanitized summaries. The Controller never streams hidden reasoning
or raw secrets.

### ChatGPT web interface

The existing transport-neutral MCP tools are wrapped by a remote MCP gateway.
The gateway is reachable from ChatGPT through Secure MCP Tunnel, while the MCP
server and Controller remain private. The gateway contains transport and
authentication code only; task state and scheduling remain in Controller.

MCP tools:

```text
submit_task
start_attempt
dispatch_task
get_task
list_tasks
get_task_events
continue_task
cancel_task
retry_task
```

`continue_task` is the only new conversational write operation. It requires a
task ID, attempt ID, message idempotency key, and message body. It rejects a
missing or non-active execution handle instead of creating a second worker.

The ChatGPT app may render a compact task-status card, but the complete audit
view remains in the self-hosted UI.

### Self-hosted Web UI

The Web UI is a separate TypeScript/React application with a small backend-for-
frontend (BFF). The browser talks only to the BFF over private HTTPS or
Tailscale. The BFF holds the Controller service credential and proxies only
allowlisted Controller operations.

Screens:

- Dashboard: task counts, readiness, capacity, and recent events.
- New Task: repository, base ref, objective, profile, execution class, and
  validation command.
- Task Detail: chat/follow-up panel, state, attempt, worker, event timeline,
  tool summaries, validation, commit, publication, and cleanup.
- Operations: restricted cancellation, retry, UNKNOWN reconciliation, and
  release reconciliation.

The UI shows tool name, status, duration, bounded output, and exit status. It
does not show chain-of-thought, provider credentials, unrestricted environment
variables, or unredacted sensitive tool output.

## Security and failure behavior

- No browser request contains a Proxmox token, CC Hub key, GitHub token, or
  OpenAI credential.
- The MCP gateway and BFF use separate service identities.
- Write operations require server-side authorization and idempotency keys.
- Only one active turn may be sent to an attempt at a time; concurrent turns
  return a conflict.
- A timeout after an ambiguous worker request becomes UNKNOWN, never an
  automatic replay.
- Provider, bridge, worker, validation, publication, cleanup, and Controller
  failures remain distinct error classes.
- The UI may offer retry only after the Controller's existing UNKNOWN and
  attempt-state rules allow it.
- No new public listener is introduced by default. MCP uses an outbound secure
  tunnel; the Web UI is private-network only.

## Delivery stages

1. Controller interaction foundation: event cursors/SSE, durable continuation,
   idempotency, and tests.
2. MCP transport and ChatGPT app: remote transport, tunnel deployment shape,
   tool parity, and MCP contract tests.
3. Self-hosted Web UI: BFF, dashboard, task detail, event stream, and guarded
   operations.
4. Live provider/UI acceptance: OpenAI regression, Muse/GLM profile routing,
   continuation, failure injection, and no-duplicate-side-effect evidence.

## Acceptance criteria

- Both interfaces create and observe the same Controller task.
- The Controller creates exactly one attempt/worker for one dispatch.
- ChatGPT follow-up reaches the same active worker or fails explicitly.
- Web UI follow-up has the same semantics as MCP `continue_task`.
- Event stream reconnects from a cursor without losing or duplicating events.
- Controller restart preserves task, message, execution-handle, and delivery
  state.
- Ambiguous sends remain UNKNOWN and are never automatically replayed.
- OpenAI direct path remains unchanged.
- No old WebCodex service, runner, or cloudflared unit is restored.
