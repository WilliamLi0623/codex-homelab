# Controller and Worker Fixed-Role Routing Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make every newly dispatched Proxmox/K3s worker use Luna/High in normal mode and GLM-5.3 Flash/MAX in confirmed Codex-quota-fallback mode, while freezing the resolved route on the attempt before any external allocation.

**Architecture:** A host-side quota coordinator publishes only `normal` or `quota_fallback`, an observation timestamp, and a monotonic generation to an authenticated Controller endpoint. The Controller persists that state. At attempt creation it resolves the worker role to a validated immutable route (provider, model, wire API, reasoning effort, public base URL, and Kubernetes Secret reference), stores the route with the attempt, and dispatches the exact stored route. The existing `internal/muse.ChatHTTPClient` remains the GLM Chat Completions adapter. No scheduler, provider credentials, or quota-detection logic is moved into the worker or bridge.

**Tech Stack:** Go, SQLite schema migrations, HTTP API, Kubernetes Job environment and Secret references, React/TypeScript UI.

**Spec:** `docs/superpowers/specs/2026-09-23-fixed-codex-role-routing-design.md`

**Related host plan:** `docs/superpowers/plans/2026-09-23-codex-host-quota-routing.md`

## Global constraints

- Fixed role table: normal worker=`gpt-6-luna` / `responses` / `high`; quota-fallback worker=`glm-5.3-flash` / `chat-completions` / `max`.
- This is one policy with two states, not a user-selectable routing strategy or per-task provider picker.
- Only the host's authenticated, authoritative quota-state publication may change the persisted Controller mode. A worker 429/503, provider outage, malformed request, network/auth error, or usage percentage must never mutate it.
- Resolve and validate a concrete route before capacity claim or Job creation. Freeze it durably for that attempt; retries and idempotent dispatch use the same route.
- Missing, stale, unauthenticated, malformed, or out-of-order mode updates fail closed: retain the last known mode; on first boot without a valid state, use normal only if normal OpenAI route is configured, otherwise reject dispatch as unconfigured.
- Never place secret values in SQLite, task/attempt responses, event payloads, Job labels, logs, or UI. Persist only the Kubernetes Secret name/key reference already supported by deployment configuration.
- Preserve attempt VM/LXC identity, workspace, credentials, deterministic branch, UNKNOWN-outcome reconciliation, cancellation, cleanup, and P25/P26 behavior.
- Existing attempts and Jobs retain their stored route across mode changes. Do not replay or reconfigure a running worker.
- Keep legacy attempt metadata readable; new task creation no longer offers model/provider choices.

---

## Task 1: Define and test the fixed route resolver

**Files:**
- Create: `internal/modelrouter/routes.go`
- Create: `internal/modelrouter/routes_test.go`
- Read/adjust only as required: `internal/modelrouter/router.go`, `internal/modelrouter/router_test.go`

**Interfaces:**
- `type Mode string` with exactly `normal` and `quota_fallback` as routable values; represent unavailable separately and fail dispatch closed.
- `type Role string` with `orchestrator` and `worker`; current Controller task attempts resolve as `worker`.
- `Resolve(mode Mode, role Role, config RouteConfig) (ResolvedRoute, error)` returns provider ID, model, wire API, reasoning effort, public base URL, and Secret name/key reference.
- The resolver has no mutable provider-health fallback and makes no network requests. Its result is deterministic for the same mode, role, and validated config.

- [ ] Add table-driven tests for both roles in both modes, asserting exact provider/model/wire/effort and preserved non-secret endpoint/Secret references.
- [ ] Add rejection tests for unknown mode/role, missing route config, GLM with Responses, Spark with Chat Completions, wrong reasoning effort, empty endpoint, and secret values accidentally supplied where only references are allowed.
- [ ] Replace the old selectable profile-chain assumptions in `internal/modelrouter` with the fixed role/mode mapping; preserve unrelated packages and do not add automatic retry/failover behavior.
- [ ] Run `go test ./internal/modelrouter -count=1`.

**Expected:** Resolver tests demonstrate only the approved two-state route table; no code path rotates models based on provider errors.

## Task 2: Persist quota mode and immutable attempt route

**Files:**
- Modify: `internal/store/sqlite.go` (add migration version 6 only)
- Create/modify: `internal/store/routing_state.go`, `internal/store/routing_state_test.go`
- Modify: `internal/store/attempts.go`, `internal/store/initial_attempt.go`, `internal/store/list_attempts.go` and their tests

**Interfaces:**
- Add singleton `routing_state` row: mode, observed_at, generation; store no account identity, token, model key, or prompt.
- Add attempt route fields: route mode/generation, provider, model, wire API, reasoning effort, public base URL, Secret name, Secret key. Never store the secret's value.
- Atomic `SetRoutingState` rejects invalid mode and any observation/generation older than the persisted value; repeating the exact same generation and payload is idempotent.
- New attempt creation records requested role and the fully resolved route in the same transaction as attempt creation. Existing rows remain readable and are marked unresolved/legacy rather than silently rewritten.

- [ ] Add migration-v6 tests from a fresh database and a database at migration v5; assert existing task, attempt, and capacity data remain intact.
- [ ] Add store tests for monotonic state writes, idempotent repeats, stale/out-of-order rejection, invalid mode rejection, and concurrent updates.
- [ ] Add attempt tests proving route snapshot and attempt record commit atomically and that legacy attempts remain readable without an invented route.
- [ ] Ensure attempt/API structs expose only safe route metadata where necessary; omit Secret references from public responses unless a concrete UI/operator need is established.
- [ ] Run `go test ./internal/store ./internal/modelrouter -count=1`.

**Expected:** A restart-safe, monotonic mode value and immutable per-attempt route snapshots; no secret values persisted.

## Task 3: Add authenticated quota-state publication endpoint

**Files:**
- Modify: `internal/api/server.go`, `internal/api/server_test.go`
- Modify: `cmd/controller/config.go`, `cmd/controller/config_test.go`, `cmd/controller/main.go`
- Reuse: `internal/store/routing_state.go`

**Interfaces:**
- Add `POST /internal/v1/routing-state`, accepting strict JSON `{mode, observed_at, generation}` only.
- Protect the endpoint with a dedicated `CODEX_ROUTING_STATE_TOKEN`, compared in constant time; missing server token disables the endpoint, missing/wrong client bearer token returns 401/403 without revealing configuration.
- Set body-size limit, reject unknown JSON fields, invalid timestamps, unreasonable clock skew, unsupported modes, and stale generations. Return only accepted mode and generation; never echo the bearer token.
- The host coordinator contract from the host plan posts only this state tuple. It does not send account IDs, prompts, Codex conversation data, or provider credentials.

- [ ] Add HTTP tests for valid normal/fallback updates, no auth, bad auth, missing server token, invalid JSON/fields, oversized request, stale observation, and idempotent duplicate.
- [ ] Verify the endpoint cannot be reached through task/public routes without the dedicated token and that existing API routes behave unchanged.
- [ ] Wire token configuration through the existing deployment secret injection mechanism; do not create a new plaintext secret file or print token values.
- [ ] Run `go test ./internal/api ./cmd/controller -count=1`.

**Expected:** Only a valid authenticated host publication can change the durable mode. All failure classes leave the prior state unchanged.

## Task 4: Resolve before allocation and dispatch frozen attempt route

**Files:**
- Modify: `internal/orchestrator/broker.go`, `internal/orchestrator/broker_test.go`
- Modify: `internal/api/server.go` and attempt-start/retry call paths/tests
- Modify: `internal/mcp/server.go`, `internal/mcp/server_test.go` only where attempt route is forwarded
- Modify: `internal/executor/k3s/executor.go`, `internal/executor/k3s/kubernetes_runtime.go`, related tests
- Modify: `cmd/controller/config.go`, `cmd/controller/main.go`

**Interfaces:**
- Add a route resolver dependency at the Controller/orchestrator boundary. It reads the persisted mode and deployment route configuration, then resolves the attempt's fixed role.
- Resolve and validate before calling `Capacity.Create`; a resolution error results in zero capacity claims and zero Kubernetes Jobs.
- Dispatch request carries a typed immutable route snapshot, not just an arbitrary model profile string. If the route is already stored for the attempt, use it exactly and reject conflicting caller-supplied route data.
- Kubernetes runtime writes model, wire, effort, public endpoint, Secret references, and route generation from that snapshot to the Job. Secret values continue to be mounted/injected through Kubernetes Secret references only.
- `cmd/agentd` behavior stays as-is: GLM continues through the existing Chat Completions internal adapter; Luna uses the existing OpenAI Responses path. Do not rebuild the worker image unless a changed worker binary is actually required.

- [ ] Add broker tests proving invalid/unavailable routes cause no `Capacity.Create` or `CreateJob` calls.
- [ ] Add tests proving normal and fallback routes become the expected Job environment and attempt labels/annotations contain no credentials.
- [ ] Add idempotent dispatch tests proving duplicate dispatch of one attempt reuses the same route and Job, even if global routing mode changed after the first dispatch.
- [ ] Add regression tests for capacity UNKNOWN behavior, release-on-known-create-failure, and no release/replay on ambiguous create outcomes.
- [ ] Run `go test ./internal/modelrouter ./internal/orchestrator ./internal/executor/k3s ./internal/api ./internal/mcp ./cmd/controller -count=1`.

**Expected:** Correct route is fixed before infrastructure allocation; mode transitions affect only newly created attempts.

## Task 5: Replace task UI provider picker with automatic worker role

**Files:**
- Modify: `ui/src/App.tsx`
- Modify: `ui/src/components/TaskDetail.tsx` if it shows a model choice or route as mutable input
- Modify: `ui/src/types.ts`, `ui/src/api/controller.ts` as needed for safe route status
- Modify: relevant UI tests and user documentation

**Interfaces:**
- New tasks request the fixed `worker` role (or the API's documented automatic-worker marker), not a model/provider name.
- Existing historical attempts may display their frozen model/provider as read-only metadata; do not relabel their history as the new route.
- New attempt UI displays “Automatic (Codex quota state)” and a read-only effective route/mode where available. It offers no user-selectable strategy or model override.
- Do not expose endpoint tokens or Kubernetes Secret references in the browser.

- [ ] Replace the OpenAI/Muse/GLM model picker with a fixed role field/label and preserve form accessibility and existing task submission flow.
- [ ] Add UI tests for creation payload, validation, legacy attempt display, and read-only current route status.
- [ ] Run the repository's documented UI test/build commands after checking `ui/package.json`; do not invent or skip an unavailable test command.
- [ ] Verify API compatibility for existing clients that send the legacy profile values: map only safe supported legacy values to `worker` for new attempts or reject with a clear 400 and migration note; never let a legacy value select a different route.

**Expected:** UI communicates automatic routing clearly and cannot override the approved fixed policy.

## Task 6: Deployment, upgrade, and rollback safety

**Files:**
- Modify relevant Controller deployment manifests/config templates and secret-injection docs identified by repository search.
- Modify: `docs/migration/p27-codex-host-routing-2026-09-23.md`
- Add focused deployment/config tests where manifests are templated.

**Interfaces:**
- Provide the dedicated routing-state bearer token to the host quota coordinator and Controller using the project's established secret mechanism; rotate independently from the model provider key.
- Controller accepts state updates only from authenticated callers and retains persisted mode through restart.
- Rollback disables the publishing coordinator/endpoint and pins new work to normal only after verifying direct OpenAI worker configuration; it never deletes attempts or rewrites their frozen routes.
- Do not update Proxmox/K3s templates, replace worker images, or restart production services until software tests, migration compatibility, backup, and rollout order are verified.

- [ ] Identify exact deployment manifests and current secret references before editing; add only the new token reference and route fields required.
- [ ] Add rollout order: schema-compatible Controller upgrade; authenticated mode endpoint; host coordinator; verify normal-mode dispatch; then enable quota-fallback publication; verify fallback dispatch; finally verify recovery.
- [ ] Document migration backup, health/readiness checks, route inspection without secrets, and rollback to normal routing.
- [ ] Verify upgrade against a copy of the current SQLite schema/data and confirm no external VM/LXC/Job mutation occurs during migration tests.

**Expected:** Upgrade and rollback preserve all existing attempts and infrastructure; mode publication can be disabled without losing the normal direct OpenAI path.

## Task 7: Cross-mode acceptance and regression evidence

**Files:**
- Modify: `docs/migration/p27-codex-host-routing-2026-09-23.md`
- Tests across resolver, store, API, orchestrator, executor, and UI.

- [ ] Run the full Go suite: `go test ./... -count=1`.
- [ ] Run documented UI tests and production build; record exact commands and tool versions.
- [ ] With mocks, verify `normal → quota_fallback → normal`: new attempts resolve Luna/High, then GLM/MAX, then Luna/High; an already-started attempt retains its original snapshot throughout.
- [ ] Inject unrelated GLM/OpenAI 429, 503, auth failure, timeout, malformed response, and bridge outage; verify none updates quota state or silently changes a route.
- [ ] Verify unauthenticated/malformed/stale state updates are rejected and the last accepted mode remains active.
- [ ] Verify route resolution failure happens before VMID/LXC capacity claim and before K3s Job creation.
- [ ] Verify P25/P26 isolation, deterministic branch, credential boundaries, UNKNOWN outcome reconciliation, completion persistence, and cleanup tests remain passing.
- [ ] If live deployment is in scope, back up the Controller database/config first, canary one non-destructive attempt in normal mode, then fallback only via an authoritative quota-state fixture/live signal; do not deliberately exhaust quota or restart active workers.
- [ ] Record each acceptance result as passed, failed, not run, or blocked with evidence; do not claim a phase complete based on unit tests alone.

**Expected:** The fixed routing policy is validated end-to-end without consuming quota/reset credits, changing active attempts, or weakening existing infrastructure safety gates.

---

## Dependency order

1. Task 1 route resolver contract and tests.
2. Task 2 persistence migration and store contract.
3. Task 3 authenticated state publication endpoint. The host plan's quota coordinator must use exactly this endpoint contract.
4. Task 4 route resolution and dispatch snapshot.
5. Task 5 UI/API compatibility can proceed in parallel after the role contract in Task 1 is fixed; integrate before deployment.
6. Task 6 deployment and upgrade only after Tasks 1–5 pass locally.
7. Task 7 combined validation and any live rollout.

The app-server quota capability gate in the host plan remains authoritative. If the installed Codex runtime cannot provide a trustworthy exhaustion/recovery signal, do not enable fallback publication; the Controller remains fail-closed and the limitation must be reported.
