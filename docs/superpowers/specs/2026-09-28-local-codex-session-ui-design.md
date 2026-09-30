# Local Quota-Routed Codex Session UI Design

> **Historical prototype:** P28 supersedes this Windows-local, quota-routed deployment target. Preserve the design and its evidence; do not treat it as the production target. See [P28 Linux Session Control Plane](../plans/2026-09-30-p28-linux-multi-backend-codex-session-control-plane.md).

## Status and scope

This design adds a local user interface for creating and interacting with Codex
App Server sessions whose initial provider is selected from the authoritative
Codex account quota state. It addresses the P27 gap that explicit App Server
`thread/start` provider selection is proven, but Codex Desktop's own new-thread
path has not been shown to consume the quota coordinator state.

This is a separate local Codex-session interface, not an extension of the
Controller task console. The existing task console continues to create and
observe durable Proxmox/K3s tasks. The local session UI uses Codex App Server
for interactive local coding sessions. It does not alter Codex Desktop,
replace the Controller, or proxy the OpenAI provider.

## Fixed routing policy

The UI offers no manual routing strategy selector. Before creating each new
thread, it requests a fresh authoritative quota observation and atomic route
decision from the single local quota coordinator. The coordinator reads the
supported App Server `account/rateLimits/read` RPC and applies the existing
`internal/codexrouting` classifier:

| Authoritative state | New main/orchestrator thread | New subagents |
| --- | --- | --- |
| Normal | OpenAI `gpt-6-luna`, `high` | OpenAI `gpt-6-luna`, `high` |
| Quota exhausted | CC Hub `muse-spark-1.3-contributor`, `xhigh`, Responses | CC Hub `glm-5.3-flash`, `max`, through the existing local Responses bridge |
| Unknown/unavailable | Keep the last authoritative state; if none exists, use the configured normal OpenAI route and visibly report that quota state is unknown | Same state as the new main thread |

Only explicit quota fields accepted by the existing classifier may select
fallback or recovery. Generic request 429/503 errors, transport failures,
authentication failures, usage percentages, and reset timestamps alone do not
change the route. A fresh quota read is required before creating a new thread.

Each thread remains pinned to its initial provider/model/effort. A quota
transition affects newly created threads and future Controller attempts only;
it never changes, replays, or cross-resumes an existing thread. The existing
direct OpenAI provider remains unchanged.

## Architecture and ownership

```text
Browser on this Windows host
        | same-origin loopback HTTP/SSE
        v
Local Codex session host (127.0.0.1 only)
  - authenticated UI/API boundary
  - authoritative quota read and shared classifier
  - publishes only mode/generation to the existing Controller sink
  - starts/resumes Codex App Server sessions
        | stdio JSON-RPC
        v
Codex App Server using the existing user's CODEX_HOME/authentication
        | OpenAI Responses or configured CC Hub Responses provider
        v
OpenAI / CC Hub
```

The local session host is a Codex App Server client and narrow route selector.
It does not implement a second task scheduler, worker database, model proxy,
or provider fallback router. The existing local quota coordinator remains the
single owner of quota polling, classification, persisted generation, and
Controller publication; the session host must obtain an atomic route decision
from that coordinator through an in-process interface or authenticated local
IPC. It reuses `internal/codexrouting` and the existing Controller mode sink
so local new sessions and Controller-created attempts consume the same fixed
policy. Do not run a second independent quota poller or allow the UI to write
Controller routing state directly. If an authoritative transition cannot be
published, the coordinator reports that synchronization failure; the UI must
not claim global consistency or start a newly routed session on the basis of
an unpublished mode.

The existing remote task UI/BFF remains Controller-only. It must not spawn a
Codex process on the user's Windows machine, receive local Codex credentials,
or be repurposed as a tunnel to the local App Server.

## Session lifecycle and user experience

- The landing view shows local Codex sessions and a visible quota/routing
  status: normal, fallback, or unknown, with observation time and the route
  selected for the next new thread.
- “New session” performs a fresh quota read, applies the fixed policy, starts
  one App Server thread with the selected provider/model/effort, then begins
  the first turn. It displays the selected route before the first turn starts.
- Existing sessions can be resumed only through their recorded App Server
  history and provider. The UI never offers an OpenAI↔CC Hub provider switch
  within a thread.
- The conversation view streams App Server events, supports sending the next
  user turn and interrupting the active turn, and presents App Server approval
  requests using the user's existing approval/sandbox policy. It does not
  weaken approval requirements or silently auto-approve tool actions.
- Tool/activity rendering is bounded and redacted. Hidden reasoning, raw
  credentials, unrestricted environment variables, and full tool output are
  not exposed by default.
- This UI is an additional local interface; the native Codex Desktop remains
  available and unchanged. Its new-thread button is not quota-aware unless a
  future, separately verified supported integration is added.

## State and persistence

Codex App Server history remains the source of truth for conversations,
messages, turns, and resumable thread identifiers. The UI must not duplicate
conversation bodies or tool outputs in a second database. Any local metadata
needed to display or enforce a thread's provider pin must be minimal, contain
no credentials or prompt text, and be recoverable from App Server history
where that interface exposes it.

Quota observation uses the existing strict classifier and coordinator state
semantics. Account identifiers, reset-credit IDs, prompts, and provider keys
are not stored in the session UI. Controller synchronization sends only
`normal`/`quota_fallback`, observation time, and monotonic generation through
the already authenticated state endpoint.

## Security and failure behavior

- Bind the local HTTP listener only to `127.0.0.1`; do not expose it on LAN,
  Tailscale, or `0.0.0.0`.
- Serve the UI and API from one origin. Validate the `Host` and `Origin`
  headers, reject cross-origin writes, require an ephemeral local session
  credential for API writes, keep that credential in process memory or a
  protected HttpOnly cookie (never browser local storage), and use bounded
  request bodies and timeouts.
- Reuse the existing Codex `CODEX_HOME` authentication and provider
  configuration. Never copy OpenAI or CC Hub credentials into browser storage,
  request payloads, logs, fixtures, or a new plaintext secret store.
- App Server child processes are started and stopped by the local session
  host with bounded lifecycle management. The service must not restart the
  user's existing Codex Desktop process.
- Quota RPC failure or malformed/ambiguous quota data is `unknown`, not
  exhausted. A model/provider error after thread creation is surfaced as a
  provider failure and never causes transparent rerouting or a duplicate turn.
- If an App Server stream ends ambiguously, preserve the thread and turn
  outcome as unknown; do not replay a potentially side-effecting turn.
- Shutdown or UI disconnect must not kill unrelated Desktop sessions. Active
  local service sessions follow explicit interrupt/continue semantics and
  retain their App Server history.

## Compatibility gates

Before implementation claims the route contract works, executable probes must
verify against the installed CLI/App Server version:

1. `account/rateLimits/read` and the existing classifier produce the expected
   normal, exhausted, recovery, and unknown outcomes.
2. `thread/start` can select exact provider, model, and reasoning effort for
   both normal and fallback routes without changing global `config.toml`.
3. Newly spawned subagents inherit the required model/provider/effort for both
   modes; if the App Server cannot scope subagent settings per thread, the UI
   must not claim that subagent routing is correct and automatic fallback
   remains disabled.
4. Resuming a saved thread preserves its original provider and history.
5. The existing GLM Responses bridge is usable with Codex's actual subagent
   tool inventory and does not leak the CC Hub key.
6. Controller route publication and local new-thread selection agree on the
   same generation; transient API failures do not guess a state transition.

Failure of a compatibility gate stops rollout of the affected automatic
route; it does not authorize modifying Codex Desktop internals or weakening
the route, history, or approval invariants.

## Acceptance criteria

1. The local UI works without changing or restarting native Codex Desktop.
2. The listener binds to loopback only and rejects cross-origin writes.
3. New threads use Luna/high in authoritative normal mode and Spark/xhigh in
   authoritative exhausted mode; unknown state is explicit and conservative.
4. New subagents use Luna/high normally and GLM-5.3 Flash/max on fallback,
   proven from effective runtime metadata rather than static configuration.
5. Existing threads remain pinned across quota transitions and resume with
   their original history.
6. OpenAI direct traffic remains unchanged; Spark uses its configured direct
   CC Hub Responses route; GLM subagents use the existing local bridge.
7. Quota errors, provider outages, and stream interruption never trigger
   duplicate or implicit cross-provider turns.
8. No credentials or hidden reasoning appear in browser state, logs, fixtures,
   or additional persistence.
9. Controller-created Proxmox/K3s attempts continue using the existing
   Controller's frozen route snapshots and P25 isolation behavior.
10. Unit, integration, UI, security-boundary, and opt-in live tests pass; the
    existing OpenAI direct path and P27 Controller/worker routing regressions
    remain green.

## Explicit non-goals

- Modifying or injecting code into the native Codex Desktop application.
- Replacing the existing task UI, Controller, MCP Gateway, model router,
  scheduler, task database, or worker runtime.
- Adding a new model proxy or copying keys to another host.
- Routing or resuming already-created Desktop conversations across providers.
- Automatically granting tool approvals or changing sandbox policy.
- Declaring P27 or production P25 complete before the existing remote,
  subagent-runtime, Controller-deployment, and isolation/failure gates pass.
