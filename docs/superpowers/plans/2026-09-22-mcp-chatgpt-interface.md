# MCP and ChatGPT Interface Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Expose the Controller task surface as a private remote MCP service that can be used by a ChatGPT custom app without restoring WebCodex.

**Architecture:** Wrap the existing transport-neutral `internal/mcp` server with a narrow MCP JSON-RPC HTTP transport. Run it privately and connect ChatGPT through Secure MCP Tunnel; keep Controller state and execution in LXC210.

**Tech Stack:** Go 1.22 `net/http`, existing `internal/mcp`, MCP JSON-RPC transport, systemd service, Secure MCP Tunnel.

**Spec:** `docs/superpowers/specs/2026-09-22-dual-interface-design.md`

## Global Constraints

- No public Proxmox, Controller, worker, or MCP listener by default.
- No credentials in MCP tool arguments or results.
- Write tools require server-side authorization and idempotency.
- `continue_task` must call the Controller continuation contract, never a second scheduler.
- ChatGPT plan/permission limits must be documented; lack of write permission is a blocker, not bypassed.

### Task 1: MCP transport contract

**Files:**
- Create: `internal/mcp/transport.go`
- Create: `internal/mcp/transport_test.go`
- Modify: `internal/mcp/server.go`

**Interfaces:**
- Implement `type Transport struct { Server *Server; Auth Authenticator }` with `ServeHTTP(http.ResponseWriter, *http.Request)`.
- Support `initialize`, `tools/list`, and `tools/call` with bounded JSON input/output.
- Map existing `Server.Tools` and `Server.CallTool` without moving task logic into transport.

- [ ] Write protocol tests for initialize, tools/list, one read tool, one write tool, malformed JSON, unknown tool, and missing auth.
- [ ] Run `go test ./internal/mcp -run 'TestTransport' -count=1` and confirm failure.
- [ ] Implement the minimal protocol and explicit error mapping.
- [ ] Run `go test ./internal/mcp -count=1`.
- [ ] Commit `feat: add remote MCP transport for controller tools`.

### Task 2: Gateway process and private deployment shape

**Files:**
- Create: `cmd/mcp-gateway/main.go`
- Create: `cmd/mcp-gateway/main_test.go`
- Create: `deploy/systemd/codex-mcp-gateway.service`
- Modify: `config.example.yaml`
- Modify: `docs/deployment.md`

**Interfaces:**
- Gateway binds `127.0.0.1` by default and accepts an explicit private listen address only through configuration.
- Gateway authenticates requests before invoking `internal/mcp`.

- [ ] Write startup/config tests proving loopback default, required store path, bounded body size, and secret-free logs.
- [ ] Run targeted tests and confirm failure.
- [ ] Implement process assembly using the existing Controller store/config boundaries.
- [ ] Add a systemd unit with restrictive user, private temporary directory, and no Proxmox credential access.
- [ ] Run `go test ./cmd/mcp-gateway ./internal/mcp -count=1`.
- [ ] Commit `feat: add private controller MCP gateway`.

### Task 3: ChatGPT app contract

**Files:**
- Create: `docs/integrations/chatgpt-mcp-app.md`
- Create: `docs/integrations/chatgpt-mcp-tool-contract.json`
- Modify: `docs/security.md`

- [ ] Document workspace plan requirements, Developer Mode, Secure MCP Tunnel, tool confirmation, and frozen tool-schema updates.
- [ ] Define examples for submit/start/dispatch/get-events/continue/cancel/retry with no secrets.
- [ ] Document that ChatGPT agent mode does not automatically imply custom-app availability and that write permission must be verified.
- [ ] Validate examples against the Go tool schemas and run a JSON syntax check.
- [ ] Commit `docs: document ChatGPT MCP integration boundary`.

### Task 4: MCP acceptance

- [ ] Run an in-process MCP client against `initialize`, `tools/list`, `submit_task`, `start_attempt`, `dispatch_task`, and `get_task_events`.
- [ ] Run a tunnel connectivity smoke test only after the MCP gateway and credentials are available; do not expose a public listener.
- [ ] Verify a ChatGPT custom app can read a task and request one guarded write action, or record the account-plan limitation as an explicit blocker.
- [ ] Record evidence in `docs/migration/p27-dual-interface-2026-09-22.md`.
- [ ] Commit `docs: record MCP interface acceptance`.
