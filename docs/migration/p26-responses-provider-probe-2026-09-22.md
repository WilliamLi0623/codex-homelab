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

No client fingerprint was altered and no further generation retries were
made. Resume these probes after the CC Hub site owner removes the block or
provides an approved API access path. The earlier 2026-09-22 HTTP 503 results
remain historical evidence and are not superseded by this edge-level 403.
