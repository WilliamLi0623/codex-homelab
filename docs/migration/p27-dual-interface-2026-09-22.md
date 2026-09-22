# P27 dual interface deployment — 2026-09-22

## Scope

P27 adds two operator-facing paths without restoring the frozen WebCodex
architecture:

```text
ChatGPT -> Secure MCP Tunnel -> 127.0.0.1:8090 -> Controller :18080
browser -> private access path -> 127.0.0.1:8081 -> Controller :18080
```

The self-hosted UI is static React served by the private BFF. The MCP gateway
and BFF contain no scheduler, task database, model router, or Proxmox/K3s
executor. The Controller remains authoritative.

## Code milestones

- `6d9b47a` — dual-interface design and implementation plan.
- `0feb2d7` — private task UI shell, BFF, systemd unit, and deployment docs.
- `038a402` — task detail, attempt/message/event views, SSE reconnect, and
  guarded continuation/cancel/retry controls.
- `bbf2686` — attempts listing API regression test.
- `8aac30f` — optional authenticated MCP-to-Controller HTTP adapter.
- `4c0c455` — BFF static asset serving and permission test.
- `dc84c6d` — BFF systemd ordering corrected to `codex-controller-v3.service`.

## LXC210 deployment evidence

LXC210 `codex-control` was not stopped. The existing Controller remained
active on `0.0.0.0:18080` and returned `/v1/ready` HTTP 200. New services were
installed and enabled:

```text
codex-mcp-gateway.service  active  127.0.0.1:8090
codex-task-ui-bff.service  active  127.0.0.1:8081
```

The deployed inputs were hash-checked before installation:

```text
codex-mcp-gateway       0985633ca6acb248bc51ad1d40e0eeb4c10667a313989796356b8bcb524d2c76
codex-task-ui-bff       3aa8debf62d9ade5c98f51652734810ac9d10c77fd1d47350ab7253c101d918d
task UI assets           4fa4cbcda5b38c62065eb43e780deaef6d55049548bec2b554f873e40ab1d8d3
```

The exact binary hashes were verified on both Windows and LXC210. Env files are
mode `0640`, owned by root
and their respective service group. Tokens were generated on LXC210 and were
not printed, committed, or sent back to the client. The MCP gateway has the
minimum shared-group access needed for the authoritative SQLite database.

## Black-box verification

- static UI `GET /` through BFF: HTTP 200;
- unauthenticated BFF API request: HTTP 401;
- authenticated BFF `GET /ui/api/tasks`: HTTP 200 with existing task state;
- MCP authenticated `tools/list`: HTTP 200 and all expected tools, including
  `dispatch_task` and `continue_task`;
- MCP authenticated read-only `get_task`: HTTP 200;
- all three systemd services active after installation;
- no external listener was added; both new interfaces bind loopback only.

## Not yet production-accepted

- Secure MCP Tunnel has not yet been connected to a ChatGPT workspace;
- no live side-effecting MCP dispatch or continuation was issued during the
  deployment smoke test;
- no external HTTPS/Tailscale static UI proxy has been configured;
- a full Go run still has the pre-existing intermittent Windows/SQLite
  `TempDir RemoveAll` failure in
  `TestNewHandlerFromEnvironmentServesReadyWithCompleteConfig`; all affected
  feature packages and the frontend tests/build pass.

These are explicit remaining gates, not claims of completed ChatGPT or
production coding-task E2E.
