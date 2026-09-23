# P26 Responses provider probe — 2026-09-22

The direct Linux probe used the existing CC Hub key source on
`100.64.2.121`. No key or raw reasoning payload was persisted.

## Current results

| Model | `/v1/responses` text | tool request | streaming |
|---|---:|---:|---:|
| `muse-spark-1.3-contributor` | 200 | 200 with `tool_choice=auto` | 200 |
| `glm-5.3-flash` | 503 `no_available_providers` | 503 | not applicable |
| `deepseek-v4.1-flash` | 503 `no_available_providers` | 503 | not applicable |

The Muse text response contained a normal Responses `message` and returned the
requested exact text. Usage included provider-reported input, output, total,
and reasoning-token counts.

## Muse tool-use

The standard function definition was:

```json
{
  "type": "function",
  "name": "get_test_value",
  "description": "Return a fixed test value.",
  "parameters": {
    "type": "object",
    "properties": {},
    "required": []
  }
}
```

With `tool_choice="auto"`, Muse returned a completed Responses item of type
`function_call`, preserving a stable `call_id`, function name
`get_test_value`, and arguments `{}`. With `tool_choice="required"`, CC Hub
returned a structured invalid-request error stating that only `auto` is
currently supported. Future Muse routing must therefore rely on prompt/tool
availability rather than forcing `required`.

The continuation used stateless full input history:

```text
user message
function_call
function_call_output(call_id=<original>, output=TEST_VALUE_42)
```

It returned HTTP 200 with the final text:

```text
Got the test value: TEST_VALUE_42
```

No `previous_response_id` state was required for this continuation.

## Muse streaming

The text stream returned HTTP 200 and emitted a coherent lifecycle including:

```text
response.created
response.in_progress
response.output_item.added
response.content_part.added
response.output_text.delta
response.content_part.done
response.output_item.done
response.completed
```

The terminal `response.completed` event was present.

## Decision

The current evidence supports direct Muse Responses routing for text,
streaming, one function call, and one function-result continuation. The
Chat-to-Responses bridge remains paused. GLM-5.3-Flash and DeepSeek V4.1
Flash are not promoted to Responses routing while their current upstream
Responses path returns `no_available_providers`.

## 2026-09-23 live recheck

A fresh Linux probe used the existing key source without printing or saving
the key. It requested GLM-5.3-Flash Responses with reasoning effort `max`,
DeepSeek V4.1 Flash Responses, and Muse Spark Responses with effort `xhigh`.
All three requests were rejected with HTTP 403 before any Responses payload
was returned. A read-only `/v1/models` request was also rejected with HTTP 403.
The response body identifies Cloudflare Error 1010, which Cloudflare documents
as an owner-configured browser-signature block. This is an edge-access result,
not a model/provider capability result; the current GLM and DeepSeek Responses
availability and reasoning-effort acceptance are therefore **unknown**.

During that edge-block check, no client fingerprint was altered and no further
generation requests were made. The 2026-09-22 HTTP 503 results remain
historical evidence. The later Muse result below establishes that Muse became
reachable for a fresh Responses request; it does not establish why the earlier
403 occurred or update GLM/DeepSeek availability.

## 2026-09-23 Muse sequential tool-call recheck

A fresh remote Linux probe sent three dependent standard function calls to
Muse Responses with `reasoning.effort=xhigh`, `tool_choice=auto`, and
`store=false`. It used only deterministic, no-side-effect functions returning
fixed marker strings. The test did not execute shell commands or create
infrastructure.

Observed result:

- HTTP 200 for all four Responses requests (three tool-call turns and one
  final-answer turn); final status `completed`.
- Tool order was exactly `get_stage_1`, `get_stage_2`, `get_stage_3`.
- All three returned call IDs were present and unique; each result was
  continued with its original call ID.
- The final text contained `STAGE_ONE_OK, STAGE_TWO_OK, STAGE_THREE_OK` in
  order.
- A second three-call run used the exact production adapter history shape:
  user item, then each `function_call` and matching
  `function_call_output(call_id=...)`. It completed all four requests with the
  same ordered markers, without replaying opaque reasoning items.
- A separate parallel-call run returned both function calls in one assistant
  response. Their distinct call IDs were preserved when both results were
  returned; the continuation completed HTTP 200 and summarized both markers.
- The probes used stateless complete input histories; no provider-side
  conversation state was required.

This proves upstream Muse Responses supports a three-call sequential loop for
these deterministic probes. It does **not** prove the production `muse.Runner`,
Controller event persistence, or full P25 task/isolation acceptance.

## 2026-09-23 Codex CLI integration probes

Codex CLI `0.155.0` was run in the existing Linux LXC3006 with an ephemeral
`CODEX_HOME`, temporary provider overrides, Muse `xhigh`, and the required
coding-agent User-Agent. The CC Hub key was read from the existing PVE key file
and piped into the LXC process; it was not placed in arguments, config, logs,
or workspace files. The disposable workspaces were under `/dev/shm`.

- Single tool/file workflow: Codex created `codex_muse_probe.txt` via terminal,
  read it back, exited 0, and returned the exact marker
  `CODEX_MUSE_RESPONSES_TOOL_OK`.
- Three dependent terminal calls: JSONL recorded three command executions
  (six start/completion lifecycle events) for `pwd`, `ls`, and `cat`; the final
  response contained the correct directory, filename, and marker.
- Two independent terminal calls: Codex ran two distinct command executions
  in the same turn and returned both results. The observed scheduling was
  serial (`start/end`, `start/end`), which is Codex's normal execution behavior;
  direct Responses probes separately verified two function calls in one model
  response with distinct call IDs.
- Fresh remote Linux `go test ./internal/muse ./cmd/agentd -count=1` passed.
  `TMPDIR`, `GOTMPDIR`, and `GOCACHE` were directed to `/var/tmp` because the
  PVE host's `/tmp` was full; no `/tmp` cleanup was performed.

The same-day remote PVE Responses probe used the required coding-agent
User-Agent, `codex_cli_rs/0.155.0 (Debian 13; x86_64) non-interactive`:

- Muse Spark with `reasoning.effort=xhigh` returned HTTP 200 in the tool-loop
  probes above.
- GLM-5.3-Flash with `reasoning.effort=max` returned HTTP 503
  `no_available_providers` for a standard function-tool request.
- DeepSeek V4.1 Flash returned HTTP 503 `no_available_providers` for the same
  Responses tool probe.

Thus GLM and DeepSeek are not currently usable through CC Hub Responses; the
known GLM Chat Completions path remains the active Controller configuration.

These results establish direct Codex CLI → CC Hub Muse Responses → terminal
execution and continuation for bounded tests. They do **not** prove OpenAI
direct regression, Controller-dispatched Muse, persisted Controller command
events, broader coding reliability, or production P25 isolation/failure gates.
