# P28 Linux multi-backend Codex session control plane

**Status:** approved replacement design baseline from the user's P28 plan.
**Scope:** Linux/Proxmox homelab; Windows is a browser client only.
**Supersedes:** P27 Windows-local quota-routed Session UI as the final deployment target.
**Preserves:** P25/P26 Controller, K3s, worker isolation, UNKNOWN/idempotency semantics, task UI/MCP boundaries.

## Goal

Provide a private Web UI for persistent interactive coding Sessions. Each logical Session can move between backend epochs without changing or cross-resuming a provider-specific Codex thread. The first backend identities are `codex-a`, `codex-b`, and `spark-glm`.

The final flow is:

```text
Windows browser → Tailnet/private access → fixed Linux Web UI and Controller
    → persistent logical Session → backend epoch → session runtime LXC
    → Codex A/B or Spark orchestrator
    → for Spark, Controller-owned GLM worker tasks in isolated worker LXCs
```

The Controller remains the only capacity manager and task/worker authority. Spark receives only a narrow Controller API for delegated work. The Web UI does not call Proxmox or Kubernetes directly.

## Product behavior

The UI offers Codex A, Codex B, and Spark+GLM. A Session pins each backend and identity to a separate epoch. A provider/account switch creates a new epoch for the same logical Session; it never changes the provider or identity of an existing Codex thread.

Implement manual switching first. A controlled switch drains the source epoch, checkpoints authoritative workspace/runtime state, persists a semantic handoff, freezes the source thread, starts the target epoch, and resumes the same logical Session/workspace. Automatic failover and failback may be added only after the manual handoff and runtime acceptance gates pass. They remain feature-gated and disabled by default until then.

Codex A and B have independent identities and quota observations. Codex A must not automatically fall back to Codex B, nor may B automatically fall back to A. Cross-account automatic routing requires explicit authorization independent of credential availability.

Quota is deterministic Controller-side software; Spark never polls quota. Unknown, unavailable, stale, malformed, or contradictory observations never trigger failover or recovery. Never convert missing values to zero or full availability.

## Superseded P27 assumptions

- Windows is a browser/client only. Do not change Windows `CODEX_HOME`, Desktop configuration, global `config.toml`, or App Server services for the production implementation.
- The Windows P27 Session UI remains a development/test harness. Preserve its implementation and evidence; do not delete it.
- A logical Session can contain multiple backend epochs. Each epoch uses exactly one backend/account identity.
- Quota may eventually trigger a controlled handoff; it never mutates an existing thread.
- P28 routing is per Session/epoch. The P27 global `quota_fallback`/`routing-state` publication is not a P28 dependency.
- Spark is an orchestrator, not a short-lived P25 worker or second scheduler.

## Architecture and component ownership

```text
Windows browser
    │ Tailnet/private HTTPS
    ▼
Fixed Linux control host
  ├── existing Controller: durable tasks, Sessions, capacity, routing policy,
  │   reconciliation, worker dispatch and events
  ├── Web UI BFF: authenticated, allow-listed API; no credentials in browser
  ├── quota monitor: deterministic A/B observations and policy inputs
  └── handoff coordinator: serialized epoch transitions
          │
          ├── persistent Session runtime LXC: Codex A or B epoch
          ├── persistent Session runtime LXC: Spark orchestrator epoch
          │        └── narrow Controller API → existing task/attempt path
          │                               → short-lived GLM worker LXC(s)
          └── workspace/session state independent of runtime VMID
```

The fixed Linux host may reuse LXC210, which already hosts the Controller and task UI BFF, but deployment placement must be verified before rollout. The browser talks only to the authenticated BFF/API. It never receives OpenAI/Codex auth, CC Hub keys, Proxmox credentials, Kubernetes credentials, or GitHub credentials.

The existing `internal/codexsession` transport, thread lifecycle, history, approvals, event multiplexing, process lifecycle, and sanitization are reuse candidates. Reuse only after Linux compatibility probes. Keep the existing task UI and `/v1/tasks` lifecycle separate from the new Session domain.

## Hard invariants

1. The Controller alone allocates/releases dynamic runtime capacity.
2. An App Server thread is pinned to one backend epoch and one identity.
3. Never resume a Codex thread under a different account/provider.
4. Never automatically replay an ambiguous state-changing operation.
5. A timeout after uncertain provisioning, dispatch, or handoff produces `UNKNOWN`.
6. Exactly one model turn may be active per backend epoch.
7. At most one epoch may be active per logical Session; enforce this durably.
8. Quota uncertainty never changes route.
9. Automatic failover stays disabled until manual handoff passes E2E.
10. Codex B is not an automatic fallback for A, or vice versa, without explicit authorization.
11. Spark is not a scheduler, task database, or capacity manager.
12. Persistent Session LXCs do not use short-lived worker cleanup semantics.
13. P25/P26 task execution, release, reconciliation, and task UI/MCP ownership remain unchanged.

## Session data model

Create a distinct Session domain. Do not force interactive Sessions into the existing `task_attempts → codex_threads` lifecycle. Inspect the current migration head and assign the next migration number; never hardcode a guessed migration version.

Required logical entities:

```text
sessions
session_epochs
session_runtime_bindings
session_handoffs
session_events
quota_observations
session_routing_policies
session_transition_requests
delegated_session_tasks
```

`sessions` stores logical identity, title, repository/workspace, lifecycle state, preferred backend, automatic-failover setting, and timestamps. Suggested states are `CREATED`, `PROVISIONING`, `READY`, `ACTIVE`, `DRAINING`, `SWITCHING`, `STOPPED`, `ERROR`, `UNKNOWN`, `DELETING`, and `DELETED`.

`session_epochs` stores Session ID, sequence, backend, identity ID, runtime binding, provider thread ID, state, start/end times, and inbound/outbound handoff IDs. Backends are `codex-a`, `codex-b`, and `spark-glm`. Suggested epoch states are `STARTING`, `ACTIVE`, `DRAIN_REQUESTED`, `DRAINING`, `HANDOFF_GENERATING`, `HANDOFF_READY`, `STOPPED`, `FAILED`, and `UNKNOWN`.

`quota_observations` stores normalized values only: identity, observation time/source/freshness, optional 5-hour and weekly remaining percentages, optional reset times, ordinary-usage eligibility, and status (`HEALTHY`, `WARN`, `DRAIN`, `EXHAUSTED`, `UNKNOWN`, or `STALE`). Missing values remain null/unknown. Do not persist raw secret-bearing telemetry payloads.

`session_handoffs` stores authoritative workspace/runtime facts and semantic continuation separately: source/target epoch, workspace commit/dirty status, Git status, validation, bounded machine snapshot, semantic handoff text/JSON, state, and timestamp. Handoff storage must not become a second unbounded transcript database.

Persist mappings from Session epoch to delegated child task, attempt, and execution handle. Keep the Controller's existing attempt/capacity invariants intact.

## Credential and identity boundary

Do not bake live account credentials into a generic LXC template. The template may contain Codex CLI, Git, build tools, `session-agent`, App Server launcher, and system packages. Identities remain external to the template:

```text
codex-a
codex-b
spark
glm
```

Phase 1 must prove a safe supported way to provision A/B auth into multiple Linux runtime instances and verify each effective identity. A UI profile label is not proof of authentication identity. Use an identity adapter that can prepare, verify, and revoke a runtime binding. Never print credentials, commit credential snapshots, or store credentials in SQLite/browser state/logs.

For Codex CLI 0.155.0, do not provision concurrent runtimes by copying the same `auth.json`: the tagged implementation serializes refreshes only inside one `AuthManager` process, while file-backed saves truncate/write the file without a cross-process lock. A Session runtime should obtain its own ChatGPT OAuth login/session (the pinned CLI exposes `codex login --device-auth`) unless a separate broker provides tested rotation serialization. The authenticated identity must still match the selected Codex A/B account. Any temporary device authorization code is a short-lived secret: do not persist it, log it, or put it in browser storage.

Do not use Codex B's identity without its owner's explicit authorization. Test safe login/refresh concurrency before allowing multiple simultaneous runtime LXCs to share an account.

## Quota monitor and routing policy

One deterministic monitor owns A/B reads, normalization, freshness, persistence, and policy evaluation. Initial reconciliation interval is 60 seconds unless Linux feasibility evidence supports a safer source-driven schedule. App Server notifications may act as hints; retain periodic reconciliation.

The monitor adapter must hide provider-specific response fields. A normalized snapshot has nullable 5-hour/weekly values, nullable resets/eligibility, identity, observation time, and freshness. Null means unknown.

Initial policy:

```text
Codex A Session → Codex A primary; Spark+GLM fallback
Codex B Session → Codex B primary; Spark+GLM fallback
Codex A ↔ Codex B automatic switching → prohibited by default
```

Warning threshold: 5-hour below 20% or weekly below 10%. Drain request threshold: 5-hour below 10% or weekly below 5%. Thresholds must be configuration, not scattered handler constants. Reaching a threshold requests a drain; it does not immediately switch or interrupt an active turn.

Initial failback requires 5-hour remaining at least 25%, weekly remaining at least 10%, definitive normal eligibility, and healthy telemetry for at least five minutes. Require hysteresis and prevent flapping.

If a supported quota source cannot be obtained safely, keep quota display explicit as stale/unavailable and keep automatic failover/failback disabled. Manual switching may still proceed.

## Controlled backend handoff

Implement manual switching first. Automatic failover/failback must call the same state machine.

```text
ACTIVE → DRAIN_REQUESTED → DRAINING → HANDOFF_READY
      → SWITCHING → target epoch STARTING → target ACTIVE
```

On a switch request, reject new turns for the source epoch but let an active coding turn finish by default. Capture Git HEAD/status, dirty state, changed files, validation, pending delegated task IDs, runtime/session metadata, and other authoritative machine state from the Controller/session agent. Do not ask the model to invent those facts.

If the source is still available, ask it to produce a bounded semantic handoff. Persist machine facts and semantic continuation separately before starting the target. The target receives the Session objective, machine snapshot, semantic handoff, bounded recent context, and the same logical workspace. Freeze the source epoch after handoff and never reopen its thread under another identity/provider.

If the source cannot generate a handoff, build a deterministic emergency handoff from Controller state, workspace state, visible history, sanitized recent events, validation, and delegated-task status. Preserve UNKNOWN if target startup outcome is ambiguous; do not create a second target epoch for the same transition generation.

## Verified Codex artifact upload and installation

Install the already verified Codex CLI bundle only after the owned generation has passed guest identity, strict host-key pinning, and network-release prerequisites. The Controller claims the generation-bound `artifact_verified` INTENT before starting any upload. It must run `verifyBootstrapArtifactBundle` against the root-private artifact cache immediately before transfer; a failed or stale verification prevents the side effect.

Use the existing Linux-only `internal/codexsession` pinned-SSH configuration and its dedicated identity, known-hosts file, and generation-specific host-key alias. Preserve `BatchMode`, strict host-key checking, identities-only, disabled agent/forwarding/password authentication, bounded connection timeouts, and literal-IP target validation. Do not add SCP fallback, trust-on-first-use, host-key discovery during upload, or relaxed SSH options. Add a separate artifact-transfer adapter; the existing App Server stdio transport remains unchanged.

The adapter creates one deterministic tar stream from the verified 44-file manifest. It accepts only manifest paths and modes, regular files, and the verified size/hash limits; it rejects symlinks, extra entries, path traversal, unsupported types, and any file whose identity, size, mode, or SHA256 changes between verification and streaming. Cap the complete archive at 512 MiB and each file at the verifier's configured limit. Do not include credentials, account state, prompts, or workspace content, and do not log archive bytes.

Send that stream in one invocation over the same pinned SSH identity to a fixed, narrowly scoped guest receiver/installer helper. Do not interpolate Session IDs, epoch IDs, VMIDs, user paths, or other untrusted values into a shell command. The only install selector is a lowercase digest derived by the Controller from the immutable generation binding and verified artifact manifest. The receiver accepts one bounded archive on stdin, independently validates the manifest, paths, modes, sizes, and hashes, and writes into a fresh, absent staging directory on the same filesystem as the final versioned install root. It never follows archive links and never overwrites an existing path. After complete validation and flush, it atomically renames the staging directory to the digest-addressed final directory. A pre-existing staging/final target is a conflict, not permission to clean or replace it.

Install the complete CLI bundle and resources under a generation/version-scoped directory such as `/opt/codex/<generation-artifact-digest>/`; preserve the bundle's relative layout. The existing App Server launcher expects `/usr/local/bin/codex`. Create that entry only when it is absent and after validating that the resolved executable is within the verified versioned directory. If the path already exists, do not replace it: read-only observation may accept it only when it resolves to the exact expected executable and the complete installed bundle verifies. Any other existing target is an explicit conflict requiring a new generation or operator resolution. Never mutate a running installation in place.

This is a single-shot operation. Do not automatically retry any transfer, including after connection loss, timeout, process exit, or lost acknowledgement. A partial transfer, uncertain remote promotion, or failed completion checkpoint remains `UNKNOWN`; it never authorizes another write. Preserve partial staging state for diagnosis. The generation-bound `Observe` path is strictly read-only: it checks the fixed generation target, exact file set, types, modes, sizes, hashes, executable resolution, and installed CLI version. It returns verified completion only for an exact match; absent, partial, extra, changed, or conflicting state remains unresolved and cannot be repaired in place. Resolve an irrecoverable partial installation by provisioning a replacement generation, not by mutating or deleting the uncertain target.

Persist only a non-secret SHA256 evidence digest binding the generation/runtime identity, Codex version-specific target layout, pinned manifest digest, and successful installed-state observation. The root installer and observer must not execute the just-installed CLI merely to print its version: the observed homelab bundle hashes are reproducible source evidence, not an independent vendor signature. Confirm the executable's reported version later during isolated runtime acceptance. Keep `artifact_verified` separate from `transport_verified`: the latter still requires a fresh strict pinned-SSH App Server initialize/initialized probe. App Server availability, account identity/authentication, and model-turn readiness remain separate gates; artifact verification proves none of them.

Required deterministic tests cover strict SSH command construction, exact manifest streaming, path/mode preservation, Unicode-safe filesystem paths, changed-file detection between verification and streaming, extra/symlink/traversal rejection, archive and per-file size limits, remote target-exists rejection, corrupted/missing/extra installed files, atomic promotion only after full verification, interrupted input, lost acknowledgement, no automatic retry, and read-only `INTENT`/`UNKNOWN` reconciliation. Opt-in live acceptance uses a newly provisioned disposable generation and records no secret-bearing output.

Non-goals: this transfer layer is not a controller, scheduler, task database, second capacity manager, PVE bootstrap service, account-provisioning mechanism, or SSH/App Server replacement. It does not edit the Codex App Server protocol, Session UI, Controller routes, quota policy, templates, existing guests, or credentials as part of this design.

## Persistent Session-LXC lifecycle and workspace

Session LXCs are not bounded workers. Introduce a separate `interactive-session-lxc` capacity class and do a read-only Proxmox inventory before selecting a VMID range/template. Do not collide with templates 3900–3902 or the existing dynamic worker pool 3000–3899. Do not assume those ranges can be repurposed.

Session states cover allocation, creation, start, readiness, active use, stop, resume, recovery, UNKNOWN, and deletion. Stopping releases runtime resources but preserves Session/workspace state. Logical Session IDs never depend on VMID. Runtime replacement must reattach/recover the same workspace and logical Session.

Separate compute runtime from authoritative workspace/session state. Do not make an LXC root filesystem the only copy of important work. Initially persistent LXC storage may back the workspace, behind an interface that can later attach a dedicated dataset.

Keep delegated GLM workers on the existing short-lived isolated worker lifecycle.

## Spark orchestrator and Controller API

Spark is a persistent backend epoch, not a P25 worker. It receives only narrow Controller operations:

```text
create_child_task
get_child_task
list_child_tasks
wait_child_task
cancel_child_task
```

Every state-changing request includes Session ID, epoch ID, and idempotency key. Spark cannot allocate VMIDs, call Proxmox/K3s directly, write the task database, or reconcile UNKNOWN outcomes. The Controller creates GLM child tasks through the existing task/attempt/worker path and persists their mapping to the orchestrating epoch.

Prove in order: one GLM child with result continuation; three dependent GLM child calls; parallel independent children. Then test child failure, cancellation, ambiguous dispatch, Controller restart, and duplicate-side-effect prevention. Do not enable automatic Session fallback to Spark before these gates pass.

## Session API and Web UI

Add a dedicated Session API instead of overloading `/v1/tasks`. The API covers Session create/list/read, start/stop/delete, turns/interrupt, manual switch, epoch/handoff history, events/SSE, and read-only quota. All writes use authorization, bounded inputs, idempotency, and explicit UNKNOWN handling.

The fixed Linux Web UI uses an authenticated BFF and private/Tailnet access. Preserve the existing task console as a separate surface. Show Session, active backend/epoch, runtime/LXC state, repository/workspace, turn state, auto-failover setting, and handoff history. Show A/B quota values, reset times, observation time, freshness, and unavailable state. Display epoch transitions explicitly; never imply that switching resumes the same provider thread.

Provide manual controls for authorized targets: Codex A, Codex B, or Spark+GLM. Hide unauthorized choices. Show drain, handoff, start, complete, failed, and UNKNOWN transition states. The browser never calls Controller/Proxmox/Kubernetes/provider endpoints directly.

## Rollout gates and feature flags

Keep independent flags:

```text
session_ui_enabled
codex_b_enabled
spark_orchestration_enabled
quota_monitor_enabled
automatic_failover_enabled
automatic_failback_enabled
cross_account_failover_enabled
```

Initial defaults keep all automatic routing and cross-account behavior off. Roll out in this order:

```text
manual Codex A → manual authorized Codex B → Session API/runtime and
authenticated Linux Web UI → quota display → manual Spark → manual handoff
→ automatic A/B-primary to Spark
→ automatic failback → optional authorized cross-account policy
```

Disable automation without disabling manual Sessions. Existing P25/P26 tasks must remain independently operational.

## Failure and acceptance matrix

Test Session create/resume, A/B identity separation, stop/restart/runtime replacement, workspace continuity, quota freshness/unknown/outage, manual handoff, emergency handoff, target failure, Spark-to-GLM sequential and parallel delegation, child failure/cancellation, Controller restart, LXC loss, App Server EOF/malformed response, stream disconnect, duplicate turns/switches/child dispatch, UNKNOWN recovery, credential redaction, and unchanged task UI/P25/P26 behavior.

No test may equate mock/schema evidence with real account identity, quota source, provider turn, or production acceptance. Report live and deterministic evidence separately. Do not claim completion until the Linux end-to-end flow and required isolation/failure tests pass.

## Production migration boundary

Functional Session work and production Controller rollout remain separate. The 2026-09-29 evidence records a production/source migration mismatch: production database schema v5 versus source migration v6, plus route configuration/secret references not found in the inspected production unit/environment. Before any production upgrade, test the exact migration on a copy of the database, verify integrity and rollback, reconcile the current GLM path and route configuration, locate authorized secret sources, back up, stage, health-test, deploy, verify P25/P26 and Session behavior, and rehearse rollback.

Do not restart/update production Controller, delete VMs/LXCs/claims, or clean artifacts to advance P28 before their explicit safety gates.

## Implementation sequence

Follow the 14 ordered Tasks in `docs/superpowers/plans/2026-09-30-p28-linux-multi-backend-codex-session-control-plane.md`: baseline/spec; Linux probes; Session schema; persistent capacity; Codex A; Codex B; authenticated Session API/Linux Web UI; quota monitor; manual handoff; Spark→GLM; automatic failover; failback; optional cross-account routing; production migration.

Each task records tests, runtime evidence, unresolved gates, and review findings in the P28 execution ledger. Automatic phases cannot precede the manual handoff and orchestration gates.
