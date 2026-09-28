# Local Quota-Routed Codex Session UI Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development` for independent implementation slices where available; otherwise use `superpowers:executing-plans`. Execute in order and keep each task reviewable.

**Goal:** Add a separate loopback-only browser UI for Codex App Server sessions. Each new thread must use the existing authoritative quota coordinator to select and pin the approved provider/model/reasoning route; native Codex Desktop and the existing Controller task UI remain unchanged.

**Architecture:** Reuse the existing `internal/codexrouting.Coordinator` as the sole quota reader, classifier, durable generation owner, and Controller publisher. Add an atomic fresh-decision API. A local session host owns one long-lived Codex App Server process/client and its thread event streams. A new React/Vite entry provides the local session UI; `ui/` task-console entry and `cmd/task-ui-bff` remain intact. OpenAI stays direct; Spark uses its existing Responses provider; GLM subagents use the already implemented loopback Responses bridge. Do not duplicate conversations or quota state.

**Tech Stack:** Go 1.24 modules and `net/http`, existing `internal/codexrouting`, `internal/agentd` process launcher (not its single-turn approval behavior), Codex CLI 0.156.1 App Server JSON-RPC, existing React/Vite/Vitest UI.

**Spec:** `docs/superpowers/specs/2026-09-28-local-codex-session-ui-design.md`

## Global constraints

- Preserve P25-04 and all later accepted P25/P26 behavior. Do not redo milestones or alter Controller, scheduler, worker isolation, MCP, or task database behavior.
- Keep the direct OpenAI path and native Codex Desktop unchanged. Never rewrite the user's global `config.toml` to route local UI sessions.
- Normal route: main and subagents use `gpt-6-luna` / `high`. Authoritative quota exhaustion route: main uses `muse-spark-1.3-contributor` / `xhigh`; subagents use `glm-5.3-flash` / `max` through the existing local bridge.
- Use only `ClassifyQuota`'s explicit recognized account signals. Unknown/error preserves the last authoritative mode; absent prior mode means normal route with visibly unknown status. Generic 429/503, percentages and reset timestamps never imply exhaustion/recovery.
- A provider/model/effort decision is pinned to a new thread. Never cross-resume, replay, or transparently reroute an existing turn/thread.
- Exactly one coordinator owns quota reads, generations, persistence, and Controller publication. The UI must not write routing state itself or start a second quota poller.
- If a fresh authoritative transition cannot be durably published, do not create a new thread on that unpublished route. Report a recoverable synchronization error.
- Bind HTTP and the existing GLM bridge to loopback only. Keep credentials out of browser storage, API bodies, logs, fixtures and new plaintext stores. Preserve current approval and sandbox policy; never auto-approve.
- Preserve existing untracked caches, build outputs and binaries; do not run cleanup commands or broad formatting.
- Do not claim subagent route correctness until effective runtime provider/model/effort is proven. If an App Server compatibility gate fails, keep automatic quota fallback disabled and document the exact boundary.

## File and ownership map

- Modify `internal/codexrouting/coordinator.go` and tests in `internal/codexrouting/coordinator_test.go` only for the atomic fresh-decision API; preserve existing `Run`/`PollOnce` contracts.
- Add `internal/codexsession/` for App Server protocol multiplexing, thread lifecycle, session route pins, and test seams. Reuse `internal/agentd.StartIsolatedCodexAppServer` (or a narrowly extracted safe process helper), but do not reuse `agentd.Client.StartTurn` because it hardcodes `approvalPolicy: never` and is single-turn.
- Add `cmd/codex-session-ui/` to compose one coordinator, App Server host, and local HTTP service. Do not merge it into `cmd/task-ui-bff`.
- Add `ui/codex.html` and a separate `ui/src/codex-*` entry/component/API/tests; keep `ui/src/main.tsx`, `ui/src/App.tsx` and Controller UI behavior unchanged except shared build wiring if required.
- Update `ui/vite.config.ts` only for multi-entry build and the local development proxy; preserve existing `/ui/api` task BFF proxy.
- Update `docs/migration/p27-codex-host-routing-2026-09-23.md` with sanitized capability evidence, rollout state and exact blockers. Do not modify global or remote config files as part of this repository plan.
- No changes to `internal/modelrouter`, `internal/api`, Controller route semantics, P25 worker execution, or the existing bridge conversion rules unless a verified compatibility defect specifically requires a separate follow-up.

## Task 1 — Probe and document App Server compatibility (hard gate)

**Files:** Read-only installed CLI/schema and current sanitized P27 migration report; then update only `docs/migration/p27-codex-host-routing-2026-09-23.md` with evidence.

- [x] Confirm active `codex --version` (`0.156.1`) and generate the experimental App Server schema into a unique temporary directory; no prior temporary data was overwritten or removed.
- [x] Inspect exact `ThreadStartParams`, `ThreadResumeParams`, `TurnStartParams`, approval request/response methods, and thread metadata. `thread/start` accepts per-thread provider/model/config; response reports effective provider/model/reasoningEffort and approvalPolicy.
- [x] In a fresh temporary CODEX_HOME with no copied credentials, create ephemeral no-turn threads for OpenAI/Luna/high, a configured `osc`/Spark/xhigh provider, and session-scoped `cch_bridge`/GLM/max. Responses reflected all requested values and kept approval policy `on-request`. This is a config/runtime-shape probe only, not an authenticated model request.
- [ ] Verify whether `thread/start.config` accepts session-scoped `model_reasoning_effort` and a `model_providers.cch_bridge` definition (base URL, `wire_api=responses`, auth requirement) without global config mutation. Do not assume arbitrary JSON config is honored just because the schema allows it.
- [ ] Verify whether this setting reaches subagents, including effective provider/model/effort from runtime events. This requires an authorized model turn and remains a hard gate for enabling automatic fallback; do not claim the required fallback subagent route works.
- [ ] Verify local resume/history returns the original pinned provider and thread history. No cross-provider resume probe that risks replay or destructive mutation.
- [ ] Inspect actual tool inventory for GLM subagents and confirm existing `responses-bridge` accepts the required tool shape using a sanitized, harmless non-live fixture or mock. A live CC Hub call is opt-in only and must not expose secrets.
- [ ] Hard gate for enabling fallback: if effective subagent provider/effort cannot be proven, implement/test only the normal route and UI plumbing; fallback remains disabled and must be visibly reported as unavailable. Stop before any provider/model override that cannot be runtime-verified. The first attempt using the existing CODEX_HOME failed during SQLite runtime initialization even after a scoped write permission was granted; do not keep probing that state directory until its cause is understood.

**Run:** `codex --version`; `codex app-server generate-json-schema --experimental --out <new-temp-dir>`; bounded isolated JSON-RPC probes.

**Expected:** a sanitized evidence table for route selection, subagent inheritance, resume, approval preservation, and bridge tool compatibility; no global config change and no credential/prompt in logs or evidence.

## Task 2 — Add atomic quota refresh and route decision

**Files:** Modify `internal/codexrouting/coordinator.go`; test `internal/codexrouting/coordinator_test.go`; add a small type in `internal/codexrouting/routing_state.go` only if needed.

- [x] Write failing tests first for fresh fallback, unknown without stored state, unknown with last published state, publication failure, and shared serialization; verify RED before implementation. Existing tests continue to cover PollOnce transition persistence and reader failures.
- [x] Add `RouteDecision` containing selected mode, observed mode, generation/time, freshness, publication state, and explicit `CanStart`; it contains no account IDs, reset IDs, provider secrets or raw quota payload.
- [x] Implement `RefreshAndDecide(ctx)` using the same `pollGate`, state load, strict classifier, generation, persist-before-publish and pending-retry behavior as `PollOnce`.
- [x] Keep `PollOnce(ctx) error` on the same internal transition path; all existing polling tests pass unchanged.
- [x] For unknown observations, return the last published mode; with no state, return conservative normal / unknown / generation zero. A persisted-but-pending state is not startable.
- [x] Persist and publish before marking an explicit route startable. A sink error retains pending state and returns `CanStart=false` with an error. Caller cancellation propagates rather than being treated as a quota outage.
- [x] `go test ./internal/codexrouting -count=1` and `go vet ./internal/codexrouting` passed. `go test -race` could not run because this Go environment has CGO disabled.

**Run:** `go test ./internal/codexrouting -count=1`; `go test -race ./internal/codexrouting -count=1`.

**Expected:** callers receive one atomic route decision consistent with coordinator state; failed or unknown reads never invent a transition; prior polling tests pass.

## Task 3 — Implement a long-lived App Server session client

**Files:** Add `internal/codexsession/protocol.go`, `process.go`, `manager.go`, `thread.go`, and focused tests under `internal/codexsession/`; reuse `internal/agentd` process launch safely where compatibility permits.

- [x] Add protocol tests first for JSONL request/response ID correlation, notifications/events, server-initiated approval requests, response errors, malformed/oversized lines, cancellation, and concurrent calls.
- [x] Implement one reader loop and serialized writer with request-ID multiplexing; never have concurrent consumers read App Server stdout.
- [x] Implement initialize/initialized and schema-verified `thread/start`, `thread/resume`, `thread/list`, `thread/turns/list`, `thread/items/list`, `turn/start`, `turn/interrupt`, and explicit server-request response methods.
- [x] For turn/start, omit approval/sandbox overrides. Never use `approvalPolicy: never`; thread start/turn tests preserve `on-request` defaults.
- [x] Route server-initiated approval requests to the owning session and validate method-specific decisions. Only request-scoped `accept`/`decline`/`cancel` are supported; decisions must be offered by the server for command approvals. Session-wide approval, policy amendments and permission-profile grants are rejected; unsupported request methods receive an explicit JSON-RPC error and are never auto-approved.
- [x] Manage App Server process environment and lifecycle: explicit CODEX_HOME, API-key env exclusion, proxy pass-through without logging, graceful stdin close, bounded termination, cancellation and stderr discard. The local App Server SQLite startup issue remains unresolved; do not claim live user-home availability.
- [x] Keep minimal thread pin metadata and pending approvals in process memory; conversation history stays in App Server. On restart, reconstruct only recognized routes from `thread/list` entries marked `threadSource=codex-session-ui`; generation is unknown/zero. A persistent no-turn thread may not appear in history and cannot yet be recovered after a process restart.
- [x] Add fake-server tests for create → turn events → approval → resume → second turn, interruption, and history-based manager recovery. The manager does not duplicate history. Live authenticated App Server/model-turn behavior remains unverified because the current user CODEX_HOME fails SQLite initialization.

**Run:** `go test ./internal/codexsession -count=1`; `go test -race ./internal/codexsession -count=1`.

**Expected:** one safe, concurrent JSON-RPC client streams events and approval requests without weakening user policy or duplicating conversation state.

## Task 4 — Compose the local loopback session host

**Files:** Add `cmd/codex-session-ui/main.go`, `internal/codexsession/http.go`, `http_test.go`, and route-config builder/tests in `internal/codexsession/`.

- [ ] Write handler tests first for loopback binding validation, host/origin validation, CSRF-resistant write auth, unknown routes, oversized JSON, timeouts, concurrent new-session requests, and secret redaction.
- [x] Compose exactly one `codexrouting.Coordinator` instance with the existing App Server quota reader, existing durable state store and existing authenticated Controller mode sink. Polling and per-new-thread refresh call the same coordinator instance; no second poller/store/generation owner.
- [x] Add explicit environment/config inputs for `CODEX_HOME`, Codex executable, Controller URL/token and routing state file. No bridge is started, so no second CCH secret source is introduced; configuration values are not logged.
- [x] Serve UI/API from one origin on `127.0.0.1` only. Validate Host/Origin on mutating calls; mint an ephemeral random local token at startup and deliver it only via an HttpOnly, SameSite=Strict cookie. No localStorage token.
- [x] Expose required health/status/session/SSE/turn/interrupt/approval endpoints with bounded request bodies, timeouts, global/per-session SSE caps and serialized turn lifecycle. A failed turn-start RPC is treated as ambiguous and cannot be replayed automatically.
- [x] On session creation validate the working directory, call `RefreshAndDecide`, construct provider configuration only from the fixed route table, require published/startable state, pin a new App Server thread, and do not retry or silently switch on failure.
- [ ] Live-verify the Task 1 session-scoped provider/config mechanism with an authenticated turn under the user's CODEX_HOME. Deterministic mocks pass; the known App Server SQLite initialization failure prevents this runtime check. The GLM fallback route is not exposed: production `FallbackEnabled` is hard-coded false until effective subagent routing passes Task 1.
- [x] Serve static files with CSP, `X-Content-Type-Options`, `Referrer-Policy`, API no-store and no external script/style origins. Listener configuration rejects wildcard/LAN addresses. The separate static UI bundle is built in Task 5.
- [x] Add httptest/fake App Server integration: normal→fallback transitions affect new threads only, existing thread route remains pinned, sink error blocks creation, and normal OpenAI route remains direct. This verifies code behavior, not live provider completion.

**Run:** `go test ./internal/codexrouting ./internal/codexsession ./cmd/codex-session-ui -count=1`; `go test -race ./internal/codexsession ./cmd/codex-session-ui -count=1`.

**Expected:** authenticated loopback service selects a fresh route through one coordinator and exposes a pinned App Server session without changing user config or Controller/task UI.

## Task 5 — Add a distinct browser UI entry

**Files:** Add `ui/codex.html`, `ui/src/codex-main.tsx`, `ui/src/codex/App.tsx`, `ui/src/codex/api.ts`, `ui/src/codex/types.ts`, styles/tests; update `ui/vite.config.ts` only for multi-page build/proxy wiring.

- [x] Add Vitest tests first for route/status display, create-session request, SSE event filtering/reconnect state, interrupt, approval request, error redaction and no-resend behavior. Real-browser mock verification also exercised streamed activity and an approval-required action.
- [x] Build the separate route/session console with sessions list, quota mode/freshness/generation, next-route snapshot, working-directory/prompt form, streamed conversation, bounded activity, interrupt and explicit approval controls. Earlier transcript turns are not rendered yet; the UI says so and does not duplicate transcript storage.
- [x] Do not display hidden reasoning, credentials, raw environment or unrestricted tool output. The server and client both bound/filter event data; React renders text as text (no raw HTML execution).
- [x] Call only local host endpoints. The browser client uses `/api/*`; it does not call Controller APIs, CC Hub, OpenAI or the task BFF.
- [x] Keep the existing task-console entry and `/ui/api` development proxy unchanged. Vite multi-page production build emits both `dist/index.html` and `dist/codex.html`.
- [x] Add UI tests for unknown/stale/fallback quota labels, no-send-on-ambiguous-response, SSE thread filtering/reconnect, one-shot-only approval and secret-free errors. Existing task UI tests remain green; browser mock confirmed the approval prompt and responsive page.

**Run:** `npm --prefix ui test -- --run`; `npm --prefix ui run build`.

**Expected:** separate local session UI builds and tests; existing Controller task UI still builds with unchanged API routing.

## Task 6 — Prove route, security and failure behavior end to end

**Files:** Add deterministic Go integration tests and UI tests; update migration report and concise runbook.

- [ ] Add a full mocked scenario: fresh normal → create Luna/high thread → receive streamed events → approval request is surfaced → user approves through existing policy → continue same thread; then mocked authoritative exhaustion → new Spark/xhigh main thread, with GLM/max subagent routing rejected/disabled until Task 1's effective-subagent runtime gate passes; then recovery → new normal thread. Existing threads remain pinned throughout.
- [ ] Verify effective subagent provider/model/effort from App Server runtime events or equivalent authoritative metadata in both modes. If unsupported, leave this acceptance item blocked and do not state the routing matrix is complete.
- [ ] Verify OpenAI direct provider is unchanged, native Desktop remains running/unmodified, `cmd/task-ui-bff` tests stay green, and the bridge is loopback-only with no key in request or logs.
- [ ] Inject quota unknown/read failure, sink publication error, bridge 401/429/503, App Server crash, malformed event, stream truncation, approval rejection, UI disconnect and cancellation. Confirm no false success, duplicate turn, auto-approval, implicit retry or cross-provider resume.
- [ ] Test `Host`/`Origin` rejection, loopback-only listener, body/line/SSE bounds, cookie flags, CSP, secret/prompt redaction and process cleanup. Verify a disconnect does not kill unrelated App Server/Desktop sessions.
- [ ] Provide a safe local launch command and stop/rollback command. State exactly which route works in the local UI vs unchanged native Desktop; do not claim Desktop quota awareness.
- [ ] Update P27 migration report with phase-by-phase evidence and the acceptance checklist; leave existing remote config, deployment, and P25/P26 isolation blockers open until their own gates pass.
- [ ] Run full relevant validation and inspect the final diff. Commit in small coherent slices, not one giant commit; never stage pre-existing untracked artifacts.

**Run:** `go test ./internal/codexrouting ./internal/codexsession ./cmd/codex-session-ui ./cmd/task-ui-bff ./internal/responsesbridge ./internal/modelrouter ./internal/api -count=1`; `go test ./... -count=1`; `go vet ./...`; `npm --prefix ui test -- --run`; `npm --prefix ui run build`; `git diff --check`.

**Expected:** mocked local UI/App Server/quota end-to-end tests pass; legacy UI/controller/OpenAI/P25 behavior remains green; live provider checks are clearly separated and opt-in; no unresolved compatibility gate is marked passed.

## Completion report

Report the completed task/commit list and exact evidence for: CLI/schema version; per-thread provider/model/effort; subagent effective route; resume pinning; approval policy; coordinator publication; UI/security behavior; OpenAI and task-console regressions; bridge behavior; full test suite; and remaining P27/P25 blockers. Explicitly label each as verified, partial, or unresolved. Do not declare P27 or production P25 complete from local UI success alone.
