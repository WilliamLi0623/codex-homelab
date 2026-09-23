# P15/P16 provider-routing checkpoint — 2026-09-20

## Current decision

- Full-coding order is OpenAI/Codex, then Muse Spark 1.3 Contributor.
- GLM-5.3 Flash is the low-cost worker replacement for MiMo and uses the
  validated `wire_api=chat-completions` bridge.
- The legacy `coding-agent` metadata remains unsupported by `codex-agentd`.

## Implemented evidence

- `internal/modelrouter` excludes GLM from `full-coding`.
- Worker routing can select `glm-5.3-flash` with Chat Completions wire metadata.
- Controller and K3s Job configuration propagate `CODEX_WIRE_API`.
- `cmd/agentd` rejects the legacy coding-agent adapter before writing a Responses config.
- `go test ./...`, `go vet ./...`, and targeted routing/configuration tests pass.

## GLM protocol evidence

- Authenticated `GET /v1/models` advertises the exact worker model ID
  `glm-5.3-flash`.
- `POST /v1/responses` returns HTTP 503 `no_available_providers` and is not a
  valid GLM compatibility result.
- `POST /v1/chat/completions` returns HTTP 200; an explicit function probe
  returns `finish_reason=tool_calls` and the requested function name. The live
  GLM coding-agent wire contract is therefore OpenAI Chat Completions with
  tool calls, not Responses or Anthropic Messages.
- K3s Job `glm-p16-wire-probe-20260920` repeated the probe with
  `GLM_CHAT_COMPLETIONS_HTTP=200` and `GLM_TOOL_CALL_VALIDATION=PASS`.

## Remaining gate

P15/P16 production routing cannot pass until `codex-agentd` implements the
Chat Completions adapter and real GLM worker tasks complete the validation/
commit contract. No unverified endpoint was added to production routing.

## Validation note

- `go test ./...` and `go vet ./...` pass.
- `go test -race ./...` is not runnable on this Windows host: the Go toolchain first
  required CGO, then reported that `gcc` is not installed. No race result is claimed.

## 2026-09-21 live profile-routing checkpoint

- Attempt profiles now propagate through API/MCP dispatch, orchestrator, and
  K3s Job metadata; a configured worker rejects a mismatching profile.
- The explicit Muse Spark Controller Job still produced no terminal tool call;
  its validation failed with an empty command output. This is the remaining
  provider/runtime capability gate for P13, separate from GLM's still
  unimplemented coding-agent adapter.

## 2026-09-21 Muse direct Responses routing update

- `muse-spark-1.3-contributor` now has a direct, bounded Responses terminal
  adapter in `codex-agentd`; it is no longer routed through the Codex App
  Server. The adapter is selected only for the exact Muse model/profile with
  `CODEX_WIRE_API=responses`.
- CCH's stateful `previous_response_id` continuation rejected a valid Muse tool
  result with HTTP 400. For the exact CCH endpoint only, the adapter uses the
  standards-compatible full input history shape instead. OpenAI's stateful
  continuation is unchanged, and GLM coding-agent remains fail-closed.
- This changes the remaining P13 condition from missing terminal adaptation to
  real CCH provider availability plus the downstream Controller acceptance
  evidence. It does not constitute P15/P16 GLM completion.

## 2026-09-21 GLM Chat Completions bridge update

- `internal/muse` now includes a Chat Completions client that implements the
  existing Responses-client seam. It translates user, assistant function-call,
  and tool-result history into Chat messages, then translates Chat tool calls
  and final text back into the existing bounded Muse runner response types.
- `cmd/agentd` accepts `CODEX_WIRE_API=chat-completions` for the exact
  `glm-5.3-flash` and `muse-spark-1.3-contributor` tool-loop profiles. The
  existing Responses path remains available for Muse and OpenAI behavior is
  unchanged.
- Live GLM evidence: one terminal call, a tool-result continuation, two
  sequential terminal calls, and streaming argument chunks all succeeded over
  `/v1/chat/completions`. The response did not require a `reasoning_content`
  field.
- Live DeepSeek evidence: plain text Chat returned HTTP 200, but a forced
  terminal tool call returned HTTP 503 twice. DeepSeek is not enabled as a
  coding worker.
- Live Muse evidence: plain text Chat may return HTTP 200, but tool-enabled
  requests and Responses requests remain unavailable. Muse is not enabled as
  the GLM Chat fallback.
- Local unit tests, full tests, vet, and a Linux `codex-agentd` build pass. The
  local desktop `127.0.0.1:17841` passthrough was not switched to GLM because
  its authenticated upstream route has not been proven to expose this bridge.

## 2026-09-21 GLM bounded worker evidence

- A public-repository bounded GLM task completed through the controller with
  `glm-5.3-flash`, `CODEX_WIRE_API=chat-completions`, and worker image
  `localhost/codex-worker:agentd-glm-chat-73d67583`.
- The worker emitted `muse.response`, `muse.tool_call`, `muse.tool_result`,
  `muse.response`, `muse.completed` and produced commit
  `a6bfab01231277471519d0dffd979ec0d3db6c1d`. Its deterministic completion
  branch was
  `refs/heads/codex/task-832106566092fb867685069f7ac0acf3/attempt-f4ac41a8dfe72ad0e6d5a4b1d2725663`.
- The Controller persisted `ValidationState=PASSED` and emitted
  `attempt.completed`, `task.VALIDATING`, `task.PUBLISHING`, and
  `task.SUCCEEDED`.
- This proves the bounded GLM Chat Completions terminal loop. An open-ended
  exploration prompt still hit the model turn limit and is not promoted to a
  general coding-agent claim.

## 2026-09-21 observation and Kueue integration

- Kueue v0.19.5 is installed in K3s control LXC 220. The controller configures
  `LocalQueue/default` in namespace `codex`, and Jobs carry
  `kueue.x-k8s.io/queue-name=default` plus CPU/memory requests.
- The observation loop now consumes a complete worker `/v1/result` while the
  HTTP agentd Job remains `Running`; waiting only for Kubernetes Job
  `Succeeded` left completed tool loops stranded indefinitely.
- Dynamic worker nodes currently require the matching local worker image to be
  imported after allocation. This is a remaining P19 bootstrap-independence
  item; the P17 probe was completed by importing the already-built image into
  its newly allocated node.

## 2026-09-23 CC Hub coding-agent User-Agent and remote GLM adapter proof

CC Hub requires requests to identify as a Codex coding-agent client. The
`internal/muse.ChatHTTPClient` previously inherited Go's default
`Go-http-client/1.1`, even though it correctly translated the existing bounded
Responses-client interface to Chat Completions. The adapter now sets:

```text
codex_cli_rs/<Codex version> (<OS name> <OS version>; <architecture>) <terminal>
```

The version defaults to the repository-locked Codex CLI `0.155.0`; explicit
`CODEX_CLI_VERSION`, `CODEX_OS_NAME`, `CODEX_OS_VERSION`, and `CODEX_ARCH`
overrides are supported. Otherwise OS release, Go runtime architecture, and
`TERM` are used, with a non-interactive terminal fallback. The key is never
included in the User-Agent.

Remote Linux verification on 2026-09-23:

- A regression test first reproduced the old default User-Agent, then passed
  with the exact configured value
  `codex_cli_rs/0.155.0 (Ubuntu 24.04; x86_64) linux`.
- `go test ./internal/muse ./cmd/agentd -count=1` passed on the remote PVE
  Linux host, and the updated `codex-agentd` built successfully there. Binary
  SHA256: `b88eb21e038737b11885d8a50f64b0049eee434510d0d89e0690e72cd4f707a2`.
- A live GLM-5.3 Flash run through the current-source local Muse adapter used
  `CODEX_WIRE_API=chat-completions` and reasoning effort `max`. It made two
  sequential terminal calls, created and reread `adapter_probe.txt` containing
  `GLM_ADAPTER_OK`, completed both tool-result continuations, passed validation,
  and created a scratch-repository commit. Events were
  `muse.response → muse.tool_call → muse.tool_result` (twice), then
  `muse.completed`; scratch commit:
  `2a33ddc4d5d10b629c7641402fde2fe9f5dcfc8f`.
- Direct upstream probes with the same coding-agent User-Agent returned HTTP
  200 for GLM Chat text, a standard `get_test_value` function call, and the
  tool-result continuation.
- The active Controller was read-only inspected and remains configured for
  `glm-5.3-flash`, `chat-completions`, and `max`, with worker image
  `localhost/codex-worker:agentd-glm-chat-p27-repo-normalize-0296fb99` and
  template VMID 3900. This running image has not yet been rebuilt or rolled
  into the persistent worker template; therefore this evidence proves the
  current-source adapter and direct GLM tool loop, not a post-change
  Controller-dispatched task. The Controller and templates were not modified.

- A broad remote `go test ./internal/... ./cmd/agentd -count=1` passed except
  for three existing `internal/git` tests that hard-code Windows
  `C:\repo` paths and fail Linux `filepath.IsAbs` checks. These unrelated
  platform-specific failures were not changed. The targeted remote packages
  above passed.
