# P27 Codex host routing capability evidence

Date: 2026-09-23
Codex CLI: `0.156.1`

The real Codex-to-bridge smoke tests below used the installed LXC3006 CLI
`0.155.0`; the quota/App Server schema probe above used `0.156.1`.

This document records sanitized evidence for the fixed role-routing implementation. It intentionally excludes account identifiers, reset-credit identifiers, access tokens, prompts, and provider credential values.

## Account quota signal

- Generated the installed CLI app-server JSON schema with `codex app-server generate-json-schema --experimental`.
- The schema exposes RPC `account/rateLimits/read` and `GetAccountRateLimitsResponse.ordinaryUsageAllowed` as nullable boolean. Its schema description says `null` means unavailable and must not be interpreted as recovery.
- `RateLimitReachedType` includes `rate_limit_reached`, `workspace_owner_credits_depleted`, `workspace_member_credits_depleted`, `workspace_owner_usage_limit_reached`, and `workspace_member_usage_limit_reached`.
- `ThreadResumeParams` accepts `threadId`, `modelProvider`, and `model`; `ThreadSettingsUpdateParams` supports `model` and `effort` but has no provider field. Therefore provider changes cannot rely on `thread/settings/update`.
- A live read through a separately launched local app-server succeeded after granting the CLI permission to initialize its local SQLite runtime. Sanitized result: `ordinaryUsageAllowed=true`, no reached type; quota is not exhausted at this observation. This observation is not a trigger for fallback and must not be treated as a future-state guarantee.
- Transient API request errors and usage percentages/reset timestamps are not substitutes for the account-level `ordinaryUsageAllowed` signal. A null or failed read preserves the current mode.

## Provider and continuation probes

All CC Hub probes below ran remotely on the Linux `proxmox-pve` host with the
required coding-agent User-Agent format:
`codex_cli_rs/0.156.1 (Linux 7.0.14-14-pve; x86_64) bash/5.2`. Credentials
were read from the existing protected `/root/cch_jp_key`; they were not
printed or persisted in fixtures.

- `/v1/models` returned HTTP 200 and advertised `glm-5.3-flash`,
  `deepseek/deepseek-v4.1-flash`, and `muse-spark-1.3-contributor`.
- Direct `/v1/responses` returned HTTP 503 `no_available_providers` for both
  GLM-5.3 Flash and DeepSeek V4.1 Flash. The earlier isolated Muse Responses
  text probe returned 200, but did not prove tool use or continuation; neither
  response behavior is relied on by the selected GLM Chat-backed route.
- `/v1/chat/completions` returned HTTP 200 for GLM text/tool use and DeepSeek
  tool use. Both returned ordinary `tool_calls` structures. GLM streaming
  delivered a fragmented tool call followed by `[DONE]`.
- A live GLM Chat probe with `reasoning_effort="max"` returned HTTP 200 and
  valid assistant text. This confirms the upstream accepts the configured
  worker reasoning level; it does not prove Codex completion through the
  bridge.
- For both GLM and DeepSeek, a second Chat Completions request containing the
  matching tool result and original call ID returned the expected value. This
  verifies upstream tool-call continuation, not Codex execution through the
  bridge.
- A loopback-only Linux bridge binary configured with the explicit coding-agent
  User-Agent passed `/healthz`, Responses streaming text, one function call,
  and a follow-up request with the matching `function_call_output`. Observed
  stream events from that build were `response.created`,
  `response.output_item.added`, one or more `response.output_text.delta`,
  `response.output_item.done`, and `response.completed`. The final
  continuation contained `TEST_VALUE_42`.
- The bridge live probe was a direct HTTP protocol smoke test. It did **not**
  use Codex to execute the requested tool, did not test 3+ sequential or
  parallel calls, and did not exercise the real Codex client’s same-thread
  OpenAI↔GLM switch.
- That remote binary predates the latest local SSE lifecycle/error-hardening
  changes. Full lifecycle ordering has passed a local fixture test but still
  needs a fresh remote bridge smoke test on the latest binary.

The Codex CLI task/thread was not modified. A disposable, per-invocation
Codex smoke task reached the local Responses bridge and exposed the actual
request shapes sent by the client. The request included Responses namespace
dynamic tools, root `reasoning` with an effort value, `store`, and `include`.
The namespace tools were not inferred from documentation; they were captured
from the live request. The bridge source accepts the root `prompt_cache_key`
and `client_metadata` fields as ignored, non-forwarded metadata, with focused
passing tests.

With the ephemeral `cch_bridge` provider explicitly configured with
`supports_websockets=false`, GLM `max`, `web_search="disabled"`, and empty
MCP configuration, the latest Linux runs passed both of these checks with
zero retries:

- text prompt returned exactly `BRIDGE_OK` and emitted `turn.completed`;
- one `command_execution` for `pwd` exited 0, returned `/root`, and emitted
  `turn.completed`.

When `supports_websockets=false` was omitted, an earlier run showed one retry
on a client-cancelled follow-up. The explicit HTTP/SSE setting is therefore
part of the current route evidence. The `[DONE]` immediate-completion fix and
its regression test are included. A heartbeat implementation emits
`response.in_progress` every 500 ms while the Chat stream is idle; an
integration test verifies it reaches the client while upstream remains open.

A real three-command turn then executed `pwd`, read `/etc/hostname`, and read
`/etc/os-release` as three separate `command_execution` items, each exit 0,
and returned the correct OS summary. However Codex emitted six reconnect
warnings during that turn. The three commands appeared exactly once each in
the captured event stream, but this is not sufficient to claim general
no-duplicate-side-effect safety or a clean uninterrupted multi-tool turn.
The bridge logs show client-cancelled streams; no provider credentials,
prompts, tool arguments, or tool output are logged. The 500 ms progress
heartbeat did not eliminate the reconnects. Multi-tool protocol reliability
remains a blocker to acceptance.

The smoke invocation set `web_search="disabled"` only through its ephemeral
CLI override. CC Hub Chat Completions cannot execute hosted Responses tools,
so this avoids sending an unsupported `web_search` tool during the protocol
probe. No saved Codex configuration was changed.

## Gate

Quota signal schema and live read: **verified**.
Provider/model switch metadata on a disposable thread: **verified**.
Remote GLM and DeepSeek Chat tool-call/result continuation: **verified**.
Bridge Responses text stream and one function-call continuation: **verified by direct HTTP smoke test**.
Codex text through the bridge: **verified (`BRIDGE_OK`, `turn.completed`, 0 retries)**.
Actual Codex command execution through the bridge: **verified for one command (`pwd` returned `/root`, exit 0, `turn.completed`, 0 retries) with explicit `supports_websockets=false`**.
Three sequential real Codex tool calls: **functionally completed in one turn, each executed once in this test; six reconnect warnings mean the clean-stream and general no-duplicate gate is still not passed**.
Clean stream/retry safety: **not passed for multi-tool turns; simple text and one-command probes passed with 0 retries using explicit HTTP/SSE configuration**.
Same-thread OpenAI↔GLM routing: **pending**.
Subagent effective provider/model/effort: **pending**.
Automatic fallback enablement: **disabled until actual Codex continuation, quota coordinator live transport, and subagent routing pass**.

## Implementation checkpoint

The repository now contains a fixed route resolver and immutable per-attempt
route snapshots:

| State | Main/orchestrator | Subagent and worker |
| --- | --- | --- |
| normal | OpenAI Responses, GPT-6 Luna, `high` | OpenAI Responses, GPT-6 Luna, `high` |
| authoritative quota exhausted | CC Hub Responses, Spark, `xhigh` | CC Hub Chat Completions, GLM-5.3 Flash, `max` |

Ordinary provider HTTP errors, usage percentages, reset timestamps, and
unavailable quota reads do not change the routing state. New attempts resolve
the policy once and persist a route generation; later mode changes do not alter
that attempt. Broker and K3s worker executor both reject non-worker routes
before capacity allocation / Kubernetes API calls. K3s Jobs receive the
persisted route, use Kubernetes `SecretKeyRef` for credentials, and include a
route fingerprint for idempotency checks without placing the referenced Secret
name in labels.

A loopback-only `responses-bridge` service is implemented for the GLM path.
It converts supported Responses requests to Chat Completions, preserves tool
call IDs and tool-result association, emits the full P27 text stream lifecycle,
rejects malformed streamed and non-streamed tool calls, rejects unknown
request fields rather than silently dropping them, and classifies client
cancellation separately. It accepts the observed namespace dynamic-tool shape
through deterministic reversible aliases, and treats `store` and `include` as
stateless request metadata. It does not schedule work, persist conversations,
or retry provider requests. Its initial hardening commit is `cc439ec` (`fix:
harden Responses bridge tool streams`); current P27 bridge updates are part of
the routing commit.

Final local verification after the coordinator restart-ordering fix, worker
role guards, and bridge hardening passed:
`go test -timeout 60s ./... -count=1`, `go vet ./...`, `git diff --check`,
`npm run test -- --run` (3 files / 10 tests), and `npm run build`. No local or
remote Codex configuration, Controller deployment, installed bridge service,
or live provider route has been changed.

The quota-state classifier, App Server quota RPC client, isolated
stdio-process transport, authenticated HTTP state sink, coordinator, and
durable mode/generation file store are implemented. A mocked integration test
drives authoritative quota observations through the Controller endpoint,
attempt snapshot, and dispatch boundary; ordinary upstream 503 leaves state
unchanged. The App Server stdio transport has not been live-tested against the
installed Codex CLI on the target host, and the quota coordinator has not been
deployed or enabled. The PVE host used for remote CC Hub probes has no Codex
binary; an alternate SSH alias was unavailable due to public-key rejection.
Automatic fallback remains disabled.

Review found and corrected a coordinator restart ordering defect: startup now
reads the current authoritative quota before publishing a persisted route
mode. A persisted generation is republished idempotently only after a fresh
matching observation; if the mode changed, the next generation is persisted
and published, while an unknown/read-error observation leaves the Controller
untouched. Regression tests first reproduced the prior restart republish gap,
then passed after the fix.

The latest Linux bridge runs passed exact text `BRIDGE_OK` and one real Codex
`command_execution` for `pwd`, which returned `/root` with exit 0 and
`turn.completed`, each with zero retries when the ephemeral provider set
`supports_websockets=false`. A three-command turn also completed successfully
with all three commands executed once and the correct final summary, but it
emitted six reconnect warnings. The 500 ms `response.in_progress` heartbeat
did not remove this behavior. Therefore three-call semantics are characterized
but clean multi-tool streaming and no-duplicate-side-effect acceptance remain
unproven. Parallel calls, file-edit/coding-agent workflow, and OpenAI↔GLM
client routing regression are pending. The deployed/test provider configuration
must explicitly disable WebSockets because the bridge only implements HTTP/SSE.
The real Codex request has now
characterized namespace dynamic tools, root reasoning effort, `store`, and
`include`; the source accepts root `prompt_cache_key` and `client_metadata` as
ignored, non-forwarded metadata. The `[DONE]` immediate-completion fix has a
regression test. No three-tool, parallel-tool, coding-agent, or
no-duplicate-side-effect safety claim is made. Reasoning content is not
exposed or persisted.
Production P25 isolation and provider-failure matrices also remain pending.

## Runtime warnings / access

The first app-server launch from the restricted shell could not initialize SQLite under the user Codex directory. The read-only quota probe succeeded only after explicitly granting the CLI access required for its runtime initialization. CLI startup also emitted a warning that it could not clean stale arg0 temporary aliases. The test process disabled the unavailable `webcodex` MCP endpoint; this did not change the user's saved configuration. No credentials or active-thread state were changed.

## Additional implementation checkpoint — 2026-09-24

- When Controller routing state is absent, a new attempt now uses the validated
  configured normal Luna/High route at initial generation 1. If that normal
  route is not configured, attempt creation still fails closed. No quota mode
  is inferred or written by this default.
- Dispatch validates a persisted route against the immutable role/model/wire/
  effort policy, not against mutable current endpoint or Secret references. A
  configuration rotation therefore does not rewrite or strand an already
  frozen attempt route.
- The production Broker rejects a missing frozen route before capacity claim.
  The Controller constructs a route-required K3s runtime, which rejects a nil
  route before any Kubernetes API request. The legacy K3s runtime constructor
  remains available to existing isolated compatibility tests; production does
  not use it.
- The quota parser now accepts both the generated schema's backward-compatible
  `rateLimits` object and `rateLimitsByLimitId`, captures primary/secondary
  percentages and reset instants for diagnostics, and discards account/limit
  identifiers. Classification still relies only on explicit
  `ordinaryUsageAllowed` and recognized `rateLimitReachedType`; 100% usage or a
  reset timestamp alone cannot change mode.
- Added isolated App Server JSONL transport tests for initialization ordering,
  malformed/missing responses, cancellation, and child cleanup. Live app-server
  transport, same-thread provider switching, effective subagent route metadata,
  and local/remote config application remain unverified and disabled.
- Added regression coverage that dispatch preserves an attempt's frozen route
  after endpoint/Secret configuration changes, and that missing initial route
  state selects only the configured normal route.

## Parallel implementation checkpoint — 2026-09-24

- Independent bridge review found a second observed namespace-tool encoding:
  nested functions may use the standard `function: { ... }` wrapper. The bridge
  now extracts its name, description, parameters, and `strict` metadata before
  applying the same deterministic reversible alias and restoring the original
  namespace and call ID. A wrapper-form round-trip regression test passes.
- Independent quota-routing review confirmed fail-closed classification,
  support for both quota response schema forms, App Server lifecycle cleanup,
  and secret-safe errors. It added a regression asserting App Server JSON-RPC
  error text cannot expose account/limit identifiers. Store review found no
  additional migration or snapshot fix necessary.
- A prior broad review report claiming route files were deleted and Broker
  guards missing was checked against this exact worktree and retracted by that
  reviewer; the claims were false. The Broker explicitly rejects missing or
  non-worker routes before capacity allocation, and production K3s rejects a
  missing/non-worker route before Kubernetes API calls.
- Integrated verification on Windows: `go test -timeout 90s ./... -count=1`
  passed; `go vet ./...` passed; `git diff --check` and
  `git diff --cached --check` passed. UI verification passed:
  `npm run test -- --run` (3 files / 10 tests) and `npm run build`.
- These are source/unit/build gates only. No saved local or remote Codex
  configuration was changed; no Controller, bridge service, coordinator, or
  worker image was deployed. Live App Server quota transport, dynamic
  same-thread OpenAI↔GLM routing, effective subagent provider/effort, clean
  three-call Codex streaming, file-edit coding-agent flow, and production P25
  isolation/failure E2E remain open. Automatic fallback remains disabled.
