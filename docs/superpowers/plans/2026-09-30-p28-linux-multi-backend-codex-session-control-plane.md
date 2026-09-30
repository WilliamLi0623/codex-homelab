# P28 Linux multi-backend Codex session control plane — implementation plan

**Status:** active replacement plan based on the user's P28 plan dated 2026-09-29.
**Design authority:** `docs/superpowers/specs/2026-09-30-p28-linux-multi-backend-codex-session-control-plane-design.md`.
**Prior implementation:** P27 Windows-local Session UI remains preserved as a development/test prototype; it is not the production target.

## Goal and invariants

Build persistent interactive sessions on the Linux/Proxmox homelab. A logical Session may contain multiple sequential backend epochs, each pinned to exactly one identity/backend. The UI offers `codex-a`, `codex-b`, and `spark-glm`. Spark delegates GLM work only through the authoritative Controller.

Preserve P25/P26 Controller, K3s, worker isolation, task UI/MCP boundaries, idempotency, and UNKNOWN semantics. The Controller alone owns dynamic capacity. Never cross-resume a Codex thread under another identity/provider or replay an ambiguous side effect. Browser clients never receive credentials. Quota unknown/stale/malformed data never triggers a transition. Persistent Session LXCs must not use short-lived worker cleanup. Automatic failover/failback stays feature-gated until manual handoff and all required E2E gates pass.

## Task 1 — Preserve baseline and establish the P28 specification

- Record branch, HEAD, tracked modifications, and untracked files; preserve all existing user work and build artifacts.
- Create the P28 design spec and this repository-local execution plan from the approved P28 source plan.
- Mark P27 Windows-local documents as historical prototype without deleting or rewriting their evidence, runbook, or implementation.
- Inspect the current Controller migration head and deployed-version evidence without changing production.
- Record a phase/task conflict scan and decisions in the P28 execution ledger.

**Acceptance:** replacement boundaries are documented; current P25/P26 behavior and all pre-existing work remain untouched; no production migration or service operation occurs.

## Task 2 — Linux feasibility probes

- Verify supported Linux Codex CLI/App Server lifecycle, A/B identity isolation, multi-runtime auth behavior, quota telemetry sources, and resume/interrupt behavior in a non-production environment.
- Verify Spark and GLM connectivity and characterize Spark tool calls needed to delegate through Controller.
- Do not print/copy credentials. Do not use friend-account credentials without explicit owner authorization. Do not depend on undocumented quota scraping.
- Record each result as verified, partial, or unresolved. If supported quota telemetry is unavailable, keep automatic routing disabled and continue manual switching.

**Acceptance:** Linux runtime and identity evidence exists; unresolved capabilities have explicit fail-closed behavior.

## Task 3 — Persist the logical Session domain

- Inspect the current migration head and add a distinct Session data model without forcing Sessions into `tasks → attempts → codex_threads`.
- Persist Sessions, backend epochs, runtime bindings, handoffs, sanitized events, normalized quota observations, routing policy, transition requests, and delegated task mappings as required by the design.
- Enforce at most one active epoch per Session; add idempotency, concurrency, restart recovery, and UNKNOWN tests.
- Do not run a production migration.

**Acceptance:** database restart tests reconstruct Session and epoch state; migration tests cover existing supported schema versions.

## Task 4 — Add persistent Session-LXC capacity

- Perform read-only Proxmox inventory before selecting a Session VMID range or template.
- Add a dedicated `interactive-session-lxc` capacity class and lifecycle, separate from bounded worker cleanup.
- Implement allocate/create/observe/start/stop/resume/replace/delete/reconcile with Controller ownership and UNKNOWN safety.
- Keep workspaces recoverable independently of a disposable runtime LXC.

**Acceptance:** create → start → stop → resume → replace runtime → delete completes without VMID collision or accidental worker cleanup.

## Task 5 — Run Codex A in a Linux Session

- Provision a dedicated Linux Session LXC, bind Codex A identity, verify effective identity, and create an App Server thread.
- Prove multiple turns, stop/restart/resume, and continuation of the same logical Session without switching the provider inside an epoch.
- Do not enable automatic failover.

**Acceptance:** authenticated Linux Codex A multi-turn session survives runtime restart with workspace and history intact.

## Task 6 — Isolate Codex B

- Add Codex B as an independent identity/runtime path; do not store auth in templates, SQLite, browser state, logs, or committed files.
- Verify A cannot read/use B credentials and B cannot read/use A credentials; verify effective identity, logout/revocation, and operator authorization.
- Do not make B an automatic fallback for A.

**Acceptance:** separately authorized A and B sessions complete live turns with no credential or identity crossover.

## Task 7 — Implement Session API and authenticated Linux Web UI

- Add authenticated Session API operations to create/list/read/continue a logical Session, select an allowed backend for a new epoch, submit turns, interrupt active turns, and inspect sanitized events.
- Build the Linux-hosted Web UI against that API: backend selection (`codex-a`, `codex-b`, `spark-glm`), per-Session conversation view, turn submission/cancellation, and session/epoch status.
- Keep browser access limited to the authenticated UI/API boundary; never send provider credentials, App Server auth, or raw runtime secrets to the browser.
- Enforce Session ownership and backend authorization server-side; reject attempts to continue an epoch under a different identity/provider and require the handoff state machine for backend changes.
- Add API/UI tests for authorization, persistence across reload, error states, concurrent turns, and preservation of existing task UI/MCP behavior.

**Acceptance:** an authorized user can create a Linux Session from the Web UI, select a permitted backend, complete multiple turns, reload and continue it, and observe only sanitized state; existing task UI/MCP remains unchanged.

## Task 8 — Add quota monitoring and read-only UI

- Implement one deterministic Controller-side quota monitor for A and B, using a supported telemetry source behind an adapter.
- Persist normalized observations only; unknown fields stay null/unknown, not fabricated zero or full availability.
- Add health, freshness, thresholds, reset times, and unavailable/stale behavior to the UI.
- Keep `automatic_failover_enabled=false` globally.

**Acceptance:** repeated observations match the supported source; malformed, stale, contradictory, or unavailable data cannot trigger routing.

## Task 9 — Implement manual backend handoff

- Implement the shared drain → checkpoint → handoff → freeze source epoch → start target epoch state machine.
- Use Controller/workspace facts for authoritative machine state; persist semantic handoff separately.
- Support deterministic emergency handoff when the source cannot generate a summary.
- Test Codex A → Spark → Codex A and Codex A → authorized Codex B; never cross-resume an old provider thread.

**Acceptance:** manual switching is durable across Controller restart, exactly-once per transition generation, and preserves the same logical Session/workspace.

## Task 10 — Add Spark-to-GLM orchestration

- Give Spark only the narrow Controller API for child task create/read/list/wait/cancel.
- Keep capacity, task persistence, worker lifecycle, cancellation, and UNKNOWN reconciliation in Controller.
- Prove one GLM child, three dependent GLM calls, parallel children, error handling, cancellation, restart, and no duplicate effects.

**Acceptance:** Spark completes a real coding session by delegating GLM workers through Controller.

## Task 11 — Enable quota-driven drain to Spark

- Reuse the exact manual handoff state machine; introduce no separate automatic switching path.
- Initially request drain when 5-hour remaining is below 10% or weekly remaining is below 5%, after supported telemetry is proven.
- Block new turns, allow an active turn to finish, then hand off exactly once.
- Never route A to B or B to A automatically.

**Acceptance:** concurrent Sessions drain independently to Spark without duplicated execution or cross-thread resume.

## Task 12 — Add automatic failback

- Use configurable hysteresis: 5-hour remaining ≥25%, weekly remaining ≥10%, telemetry definitively usable, healthy continuously for at least five minutes.
- Drain Spark at a safe turn boundary and create a new primary-account epoch.
- Test repeated transitions and prove no route flapping.

**Acceptance:** repeated A → Spark → A cycles preserve workspace, audit trail, and no-duplicate semantics.

## Task 13 — Optional cross-account routing

- Keep A↔B automatic routing disabled by default.
- Implement only with an explicit, independently authorized policy; credential availability is not authorization.
- Audit the policy decision and identity used for every transition.

**Acceptance:** tests reject cross-account routing without explicit authorization and audit authorized transitions.

## Task 14 — Production Controller migration and rollout

- Keep functional development separate from production migration.
- Test exact migration against a copy of the deployed database; verify integrity and rollback.
- Reconcile production route configuration and the existing GLM path with current source requirements; locate authorized secret sources without exposing values.
- Back up, stage, health-test, deploy, verify task UI/MCP/P25/P26/session paths, and rehearse rollback.
- Do not destroy or broadly clean existing LXC/VMs, claims, artifacts, or caches as part of this plan.

**Acceptance:** production migration is reversible and existing P25/P26 operations remain green; full Linux Session E2E and failure matrix are recorded.

## Execution order and stop gates

Execute Tasks 1–14 in order. Within a task, run tests before behavior changes where practical and record exact evidence. Production migration, credential provisioning, cross-account use, and destructive infrastructure actions must remain behind their explicit authorization and safety gates. If a supported quota source cannot be proven, continue manual handoff but keep automatic failover/failback disabled. If a task is blocked, finish safe work in that task and record the blocker; do not mark downstream acceptance as passed.

## Final acceptance

P28 is complete only when the Linux Web UI can create and continue persistent Sessions; Codex A/B identities remain isolated; manual handoff works; Spark delegates real GLM work through Controller; quota-driven failover/failback pass their feature-gated tests; workspace/session state survives runtime replacement; no ambiguous operation is replayed; browser/logs contain no credentials; and existing P25/P26 task execution remains unaffected.
