# ChatGPT MCP app integration

This integration provides a ChatGPT-facing view over the existing Controller;
it does not restore WebCodex and does not move scheduling into ChatGPT.

```text
ChatGPT custom MCP app
        -> Secure MCP Tunnel
        -> 127.0.0.1:8090 codex-mcp-gateway
        -> Controller SQLite / Controller API
        -> K3s and Proxmox execution
```

## Prerequisites

1. Install and configure `codex-mcp-gateway` on the private Controller host.
2. Keep `MCP_GATEWAY_DATABASE` pointed at the authoritative Controller database.
3. Set `MCP_GATEWAY_CONTROLLER_URL` to the existing private Controller URL
   when write-through dispatch/continuation is required. Optionally set
   `MCP_GATEWAY_CONTROLLER_TOKEN` if the Controller requires service auth.
4. Generate a separate high-entropy `MCP_GATEWAY_TOKEN`; never reuse a Proxmox,
   Kubernetes, CCH, GitHub, or OpenAI credential.
5. Keep the gateway on loopback and use an outbound Secure MCP Tunnel when
   ChatGPT must reach it. Do not publish port 8090 directly.
6. Verify that the ChatGPT workspace/account has Developer Mode and custom MCP
   app support. Plan availability and write-action permissions are account and
   workspace policy, not something this repository can grant.

OpenAI documents custom MCP apps through the Apps SDK and Developer Mode. Write
actions are confirmation-gated, and private MCP servers can be connected through
the Secure MCP Tunnel without opening inbound network access:

- [Build with the Apps SDK](https://help.openai.com/en/articles/12515353-build-with-the-apps-sdk)
- [Developer mode and MCP apps in ChatGPT](https://help.openai.com/en/articles/12584461-developer-mode-and-mcp-apps-in-chatgpt)
- [Secure MCP Tunnel](https://developers.openai.com/api/docs/guides/secure-mcp-tunnels)

## Protocol and authentication

The gateway accepts bounded JSON-RPC HTTP `POST` requests at `/mcp` and supports
`initialize`, `tools/list`, `tools/call`, `ping`, and the
`notifications/initialized` notification. Every request except notifications
must carry:

```text
Authorization: Bearer <MCP_GATEWAY_TOKEN>
Content-Type: application/json
```

The checked-in tool contract is
`docs/integrations/chatgpt-mcp-tool-contract.json`. Tool results contain
sanitized task state and event metadata only; they do not contain credentials,
raw environment variables, or hidden reasoning.

## Tool usage

Typical read-only flow:

1. `submit_task` with repository, base ref, objective, and an idempotency key.
2. `start_attempt` with the returned task ID and an explicit profile when needed.
3. `get_task` and `get_task_events` to observe durable state.
4. `list_tasks` to reconcile the task list after reconnecting.

The append-only `send_message` tool records a message for audit purposes. The
worker continuation path is `continue_task`, which additionally requires the
active attempt ID and a message idempotency key. A repeated key returns the
existing delivery record and never resends the message. A transport failure is
reported as `UNKNOWN`; it is not automatically replayed.

When `MCP_GATEWAY_CONTROLLER_URL` is configured, the gateway uses an authenticated
HTTP adapter to call the existing Controller dispatch and continuation endpoints.
The adapter owns no scheduler, task state, or execution handles; the Controller
remains authoritative. If the URL is omitted, `dispatch_task` and
`continue_task` fail closed. This preserves the safe read-only gateway mode for
installations that have not yet connected it to the live Controller.

## Confirmation and recovery rules

- Treat `submit_task`, `start_attempt`, `dispatch_task`, `continue_task`,
  `cancel_task`, and `retry_task` as writes.
- Show the target task ID, attempt ID, repository, and intended operation before
  a write confirmation.
- Never place a secret in tool arguments to make an operation work.
- If a tool returns `UNKNOWN`, inspect `get_task_events` and reconcile through
  the existing Controller rules; do not retry the same side-effecting request
  with a new key.
- If the gateway is unavailable, use the self-hosted UI/BFF or the loopback
  Controller API. Do not expose the Controller or Proxmox API to ChatGPT.

## Local smoke probe

With the gateway running locally, this probes authentication and discovery
without creating a task:

```powershell
$body = '{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}'
Invoke-RestMethod -Method Post -Uri http://127.0.0.1:8090/mcp `
  -Headers @{ Authorization = "Bearer $env:MCP_GATEWAY_TOKEN" } `
  -ContentType 'application/json' -Body $body
```

Do not paste the token into shell history or documentation.
