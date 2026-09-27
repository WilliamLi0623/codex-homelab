# Fixed Codex Role Routing Design

## Status and scope

This design defines one automatic role-routing rule across the local/remote Codex runtime and the homelab task workers. It does not add user-selectable routing strategies. The repository remains on `codex-homelab-v3`; this design does not authorize unrelated infrastructure cleanup or changes to completed P25/P26 work.

The implementation is split into two deliverables because the interactive Codex runtime and the Proxmox/K3s worker are separate execution systems:

1. Codex main-session and subagent defaults, including quota-triggered provider/model switching where the host runtime supports it.
2. Controller-dispatched task worker profiles, resolved into a concrete model and transport per attempt.

## Fixed routing rule

There is one policy with two automatically selected runtime states:

| State | Codex main session / orchestrator | Codex subagents | Proxmox/K3s task worker |
| --- | --- | --- | --- |
| Normal | OpenAI `gpt-6-luna`, reasoning `high` | OpenAI `gpt-6-luna`, reasoning `high` | OpenAI `gpt-6-luna`, reasoning `high` |
| Codex account quota exhausted | CC Hub Muse `muse-spark-1.3-contributor`, reasoning `xhigh`, Responses | CC Hub GLM `glm-5.3-flash`, reasoning `max`, Chat Completions | CC Hub GLM `glm-5.3-flash`, reasoning `max`, Chat Completions |

The normal OpenAI provider path remains the existing direct OpenAI path. The fallback provider uses the existing CC Hub credential injection; no key is stored in this repository, profile metadata, attempt record, Job manifest, or diagnostics.

## Quota-state contract

The switch is permitted only on an authoritative signal that the shared Codex account quota is exhausted. A temporary request/token rate limit, model overload, network error, provider outage, authentication failure, malformed request, or usage warning alone must not activate fallback.

The implementation must identify and validate an available structured quota signal before enabling automatic switching on a host. If the Codex runtime or account status source on that host does not expose a reliable signal, automatic switching on that host remains disabled and the quota failure is surfaced clearly; it must not infer exhaustion from generic HTTP status alone.

When a fresh authoritative status reports that the exhausted quota window has reset, routing returns to the normal state for new work. A state transition is recorded without secrets or prompt content. One transition applies consistently to newly started main-session turns, newly spawned subagents, and newly created task attempts. Already-running requests or subagents are not replayed or silently restarted. Each conversation thread remains pinned to its selected provider; route changes apply only to newly created threads. Do not resume a thread on another provider because provider-specific reasoning/history has already been rejected by OpenAI during the observed Spark → OpenAI continuation.

## Components and boundaries

### Codex host configuration

- Configure the local and each reachable remote Codex installation with normal-role defaults and fallback-role settings.
- Keep the existing direct OpenAI provider intact.
- Apply changes to future turns and future subagents unless runtime tests prove safe same-thread updates.
- Back up each user-level configuration before edits and preserve unrelated providers, MCP servers, profiles, hooks, and approval settings.
- Do not print authentication configuration or provider secrets during inspection, backup, diff, or verification.

### Controller route resolution

- Treat the attempt's requested profile as a role (`orchestrator` or `worker`), not a second user-selectable policy.
- Resolve the active fixed-routing state to a concrete route before creating a Job.
- Freeze the resolved route for an attempt so retries/idempotent dispatch cannot silently drift to a different model.
- Route metadata may include model, provider, wire API, reasoning effort, public base URL, and Kubernetes Secret reference only. It must not contain secret values.
- Unknown routes and invalid model/transport/effort combinations fail before capacity is claimed or a Job is created.
- Existing `internal/muse.ChatHTTPClient` is the GLM Chat Completions adapter for the worker tool loop. GLM must remain on `chat-completions`; Spark uses Responses. OpenAI remains on its existing Responses path.
- Do not replay a worker attempt after ambiguous provider failure once tools may have executed. Existing attempt isolation and result/commit boundaries remain authoritative.

### Quota-state integration

- Keep account quota observation separate from model transport and from the Controller scheduler.
- Prefer an existing structured Codex runtime/account-status signal. Do not add an undocumented account API or scrape UI state without a separately reviewed interface and security boundary.
- Propagate only a non-secret state (`normal` or `quota_fallback`) and transition timestamp/version to components that need to select a route.
- If the local and remote Codex hosts cannot share authoritative state safely, each host reports its own state and the Controller uses only the state explicitly supplied by its trusted local configuration channel; it does not guess based on a worker's unrelated 429.

## Failure and recovery behavior

- Confirmed quota exhaustion selects Spark for new main/orchestrator work and GLM for new subagent/task-worker work.
- A transient 429/503 or any non-quota failure is reported using its original failure class and leaves routing state unchanged.
- If Spark or GLM is unavailable, fail closed with a provider failure; do not silently route to an unknown model or replay side-effecting work.
- When quota recovery is confirmed, new work returns to OpenAI Luna for all roles. Existing attempts retain their frozen route.
- Bridge/adapter failures must never be reported as successful task completion.

## Security and compatibility invariants

- OpenAI direct routing remains available and unchanged in normal mode.
- CC Hub keys remain in the existing secure secret source and are injected only at runtime.
- Logs contain role, route ID/model, state transition, request/attempt correlation ID, status/error class, and timing; they do not contain keys, full prompts, or tool output by default.
- K3s worker isolation, per-attempt workspace/credentials, UNKNOWN-outcome handling, deterministic branches, and cleanup semantics remain unchanged.
- No Proxmox/K3s infrastructure is destroyed or recreated as part of implementing routing.

## Acceptance criteria

1. Local Codex reports `gpt-6-luna/high` for the main session and newly spawned subagents in normal mode.
2. Each reachable remote Codex host reports the same normal role mapping, or is explicitly reported as inaccessible/unconfigured.
3. Verified quota exhaustion switches only new work to Muse Spark `xhigh` for the main role and GLM `max` for worker roles.
4. Temporary 429, 503 overload, provider/network outage, auth, and invalid-request failures do not switch quota state.
5. Confirmed quota reset restores normal routing for new work.
6. Runtime switching does not lose conversation history, restart Codex implicitly, replay active tool calls, or mutate already-running subagents. If same-thread switching is unsupported, this limitation is surfaced and a safe next-turn behavior is documented.
7. Controller/K3s tests prove each role resolves to the expected model, wire API, effort, base URL, and Secret reference; manifests and logs contain no secret values.
8. GLM executes through the existing Chat Completions adapter; Spark and direct OpenAI use their verified Responses paths.
9. Existing OpenAI path, attempt idempotency, P25 isolation, and P26 evidence remain intact.

## Known implementation gate

The local Codex CLI is `0.156.1`; its current user config selects Luna/high for the main model and Luna/medium for subagents. The target normal state is Luna/high for both. A live quota-status read showed the account was not exhausted at inspection time. The repository has no production call site for `internal/modelrouter`, and SSH inspection of the configured `codex-worker` host did not return usable remote configuration. Therefore runtime hot-switch support, reliable quota-event shape, and remote-host configuration must be verified before enabling automatic fallback. These are acceptance gates, not permission to replace them with generic status-code heuristics.
