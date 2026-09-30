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

- Read-only Proxmox inventory on 2026-09-30 found no current cluster resources in VMID 4000–4999; `/cluster/nextid --vmid 4000` returned 4000. Reserve 4000–4999 for `interactive-session-lxc`, separate from worker VMIDs 3000–3899, templates 3900–3902, and existing special LXC3990. Keep allocator boundaries explicit and collision-tested.
- Use LXC3900 (`codex-template-base`) as the initial Session template candidate; verify its current CLI/App Server contents before cloning. LXC3901 is the fixed template and LXC3902 the GLM template; do not mutate templates in this task.
- Storage inventory showed `local` at `/var/lib/vz` on the root ext4 filesystem backed by NVMe (`ROTA=0`), while `/pool` is an ONLINE ZFS pool of two HDD mirrors (`ROTA=1`). Put Session system/root disks on `local` SSD; keep workspace/data on `pool` only when the lifecycle implements a separate durable data mount. New dynamically allocated Session LXCs use DHCP, consistent with the user's fixed-vs-dynamic IP rule.
- Read-only rootfs inventory found no Codex CLI/App Server executable or root auth file in templates 3900–3902. Template 3900 is stopped and PVE rejects starting template guests; inspection mounts were unmounted and all templates remained stopped. Do not treat the candidate as Codex-ready: provide a verified runtime bootstrap or a different approved source before Task 5.
- Keep committed migration 7 immutable. Add runtime-binding reservation/history constraints in migration 8, including reserved-range DB guards, single current binding per epoch, unique live VMID, archived deleted runtime generations, and restart-safe replacement.
- Add a dedicated `interactive-session-lxc` capacity class and lifecycle, separate from bounded worker cleanup.
- Implement allocate/create/observe/start/stop/resume/replace/delete/reconcile with Controller ownership and UNKNOWN safety.
- Keep workspaces recoverable independently of a disposable runtime LXC.

**Current implementation checkpoint:** the SQLite reservation/history slice and isolated Proxmox Session runtime manager/adapter exist in source with fake lifecycle and HTTP tests. The manager owns only VMIDs 4000–4999, verifies the configured source is a Proxmox template before cloning, keeps the root disk on `local`, forces DHCP, and allocates a separate persistent workspace volume on `pool`; deletion/replacement detaches and verifies the exact `/workspace` mount before destroying the LXC, and replacement keeps the same VMID reserved across generations. The unsafe legacy Store API that could reassign a deleted binding to another VMID has been removed. Controller/API wiring and live Proxmox create/start/stop/delete remain open; no live Proxmox mutation has run.

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

## Status addendum — 2026-09-30 continuation

- Controller can now optionally construct the persistent Session runtime manager from `SESSION_RUNTIME_TEMPLATE_VMID` and `SESSION_WORKSPACE_SIZE_GIB`, reusing the existing Proxmox credential source. Root storage is fixed to `local` (SSD); workspace storage is fixed to `pool` (HDD).
- The manager is injected into the API server through a separate constructor. Existing task routes are unchanged; no Session API route is registered until the authenticated Task 7 boundary is implemented. An enabled-runtime integration test confirms `/v1/ready` remains healthy and `/v1/sessions` remains unavailable (404).
- Validation after this slice: `go test ./... -count=1`, `go vet ./...`, and `git diff --check` pass. No Proxmox mutation or LXC lifecycle operation was run.
- Task 4 remains in progress: controller construction is wired, but authenticated lifecycle API and live Proxmox lifecycle are not yet implemented. Templates 3900–3902 still lack Codex CLI/App Server and need a verified bootstrap path before Task 5.
- Task 2 remains partial: LXC3006 reports Codex CLI 0.155.0 and `codex login status` says logged in. Invoking App Server with documented `initialize`/`initialized` NDJSON exits 0 without emitting the initialize response; no thread or model request was made. Therefore App Server handshake remains unverified.

### Task 2 probe correction — interactive App Server verification

- The prior piped probe closed stdin immediately after sending both initialization messages and is superseded. With stdio kept open, `initialize` returned successfully; only then `initialized` was sent.
- On LXC3006, `model/list` included `gpt-6-luna`. `thread/start` created thread `01a0f0f6-6ab9-7320-aa89-93f163d9750c`; three simple turns returned `P28_APP_SERVER_OK`. The App Server process was stopped and restarted; `thread/resume` returned the same ID and prior turns, and a follow-up turn again returned the exact token.
- The server emitted `account/rateLimits/updated` for the logged-in Plus account (5-hour and weekly windows). The observed usage was 1% / 17% after the probe; this is a point-in-time observation, not proof of A/B identity mapping or quota monitor correctness.
- This proves a single logged-in Linux CLI/App Server can start, stream a turn, persist history, and resume after process restart on LXC3006. It does not prove the dedicated Session LXC template/bootstrap, identity A/B isolation, tool execution, or production Controller lifecycle. Task 2 remains partial; Task 5 remains unstarted.

### Task 2 provider probe — CC Hub Spark and GLM

- On PVE, used the existing CC Hub key without printing or persisting it and sent requests with `codex_cli_rs/0.155.0 (Linux Ubuntu 24.04; x86_64) p28-probe`.
- Muse Responses text returned HTTP 200 with `completed` and `CCH_SPARK_OK`; GLM-5.3-Flash Chat Completions text returned HTTP 200 with `CCH_GLM_OK`.
- Responses tool call with `tool_choice=required` returned HTTP 503 with the explicit message that only `auto` is supported. This was a request-option limitation, not a general Spark outage.
- With `tool_choice=auto`, Muse returned a standard `function_call` (`get_test_value`, stable `call_id`, `{}` arguments). Replaying the full stateless input history, all prior output items, and the matching `function_call_output` returned HTTP 200 and final text containing the simulated result.
- GLM Chat Completions returned a standard `tool_calls` entry with an ID; returning the assistant tool-call message and a `role=tool` result using the exact ID returned HTTP 200 and final text. All tool execution was a harmless simulated value.
- These were non-streaming protocol probes, not Controller delegation or production E2E. Task 2 remains partial for A/B account attribution/isolation, concurrent auth behavior, and real Spark→Controller child-task orchestration.

### Task 4 bootstrap blocker — 2026-09-30

- Re-inspected the Controller deployment and guest-bootstrap surfaces. The Controller currently receives a scoped Proxmox API token and the Session runtime adapter uses only the Proxmox HTTPS API. No Controller-owned SSH identity, guest-exec agent, or PVE-side bootstrap service is configured in the repository deployment.
- The existing authorized-key workflow is driven from Windows and explicitly keeps the private key on Windows. Existing `pct exec`/`pct push` paths are invoked by Windows bootstrap scripts or a PVE-side preparation script; neither is a Linux Controller guest-bootstrap interface.
- Session templates 3900–3902 remain frozen and the recorded rootfs inspection found no Codex CLI/App Server. The existing PVE API clone/config path cannot by itself install Codex into a clone or provide the Controller with a guest SSH credential.
- Therefore Task 4's manager/API wiring is not sufficient to expose Session creation: doing so would allow creation of a running but non-Codex-ready LXC. No Session LXC was created and no API endpoint was enabled.
- **Decision needed before Task 4 can pass:** choose an approved bootstrap boundary: (A) build and reserve a dedicated immutable Codex-capable Session template (new template ID/capacity inventory and controlled template build); (B) authorize a separate least-privilege Controller-owned SSH bootstrap identity and explicitly provision its public key into the Session image; or (C) design a narrowly scoped PVE-side guest-bootstrap service. Do not use the Windows private key, clone an authenticated test LXC, or give the Controller unrestricted PVE host shell access.
- Until that decision, safe work may continue on isolated API/auth and test seams that do not create/start an LXC; live Session lifecycle and Task 5 remain gated. This is an architecture/security boundary, not a transient test failure.

### Task 2 streaming and GLM Responses probe — 2026-09-30

- Live probes ran from PVE with the required coding-agent User-Agent. The existing CC Hub key was read on PVE into process memory only; it was not printed or persisted.
- Muse POST /v1/responses with stream=true: HTTP 200; received response.created, response.in_progress, output-item/content-part events, one response.output_text.delta, and terminal response.completed; assembled exact text P28_SPARK_STREAM_OK.
- GLM-5.3-Flash POST /v1/chat/completions with stream=true: HTTP 200; 38 data chunks plus finish_reason=stop; assembled exact text P28_GLM_STREAM_OK.
- GLM-5.3-Flash POST /v1/responses returned HTTP 503 for both minimal text and function-tool probes. The function continuation was not attempted because no response/tool call was returned. This confirms current GLM routing should use Chat Completions; Muse Responses remains independently usable. These are provider probes, not Codex/App Server or Controller E2E.

### Task 2 — streamed tool-loop correction and verification

- With Muse Responses streaming and reasoning effort xhigh, the model returned one standard function call with response.function_call_arguments.delta/done and a terminal response.completed.
- An initial continuation probe returned 503 because the test harness placed a raw string inside the Responses input array. This was malformed test input, not an upstream stream/tool-loop failure. Replaying the user turn as a typed message item, all original response output items, and the same call_id fixed the request.
- Corrected Muse stream → function call → simulated function output → streamed continuation returned HTTP 200, three text deltas, terminal response.completed, and the expected fixed result.
- GLM-5.3-Flash Chat Completions with reasoning_effort=max returned a streamed standard tool_calls entry; the assistant call and simulated tool result with its unchanged call ID continued to finish_reason=stop and the expected final text.
- No real tool was executed. This proves the upstream protocol loop only; Codex execution and Spark-to-Controller child-task delegation remain unverified. GLM Responses still returned HTTP 503 in the separate probes above.

### Task 4 — Proxmox bootstrap alternatives researched

- Proxmox documents ssh-public-keys for LXC creation, while the clone command's documented option set does not include it. Proxmox staff confirms there is no general API endpoint for arbitrary guest command execution; pct exec and pct push are CLI-only. References: [Proxmox pct manual](https://pve.proxmox.com/pve-docs-9-beta/pct.1.html), [Proxmox staff response](https://forum.proxmox.com/threads/execute-command-in-node-with-api.112290/).
- Proxmox's LXC interfaces API returns interface MAC and IPv4/IPv6 addresses for running containers, providing a possible DHCP address discovery path: [applied Proxmox development change](https://lore.proxmox.com/pve-devel/9724b837-687f-4e83-89a3-547f0a918009%40proxmox.com/).
- Candidate that avoids mutating templates: create from a standard rootfs archive through the LXC create API, inject a newly generated per-Session SSH public key at creation, discover its DHCP IP from the interfaces endpoint, then install Codex through SSH. This differs from the current clone adapter and still requires proof of archive availability, API permissions, network reachability, and cryptographically trustworthy guest SSH host-key validation before any subscription credential is provisioned; TOFU is not approved for that boundary.
- Alternative remains a narrowly scoped PVE-side bootstrap service that uses host-local pct exec/push. It avoids SSH host-key bootstrap but adds a privileged host service and requires its own least-privilege protocol and security review.

### Task 4 — disposable rootfs/SSH bootstrap spike — 2026-09-30

- With explicit approval, created only disposable privileged LXC 4000 from a temporary Ubuntu 24.04 rootfs archive; no frozen template or Controller source was changed. The archive contained a unique pre-generated SSH host key and a temporary client public key. `ostype=unmanaged` was necessary because Proxmox's normal post-create hook rewrites host keys and applies guest network setup; the unmanaged plugin skips both.
- The test exposed two required image details: enable `nesting=1` (matching the existing Codex LXCs) so systemd-networkd can create its service namespace, and make the `.network` profile readable (`0644`). With those in place the guest acquired DHCP `10.58.2.51/24`; the Proxmox interfaces API reported the same address.
- Generated the SSH client private key inside Controller LXC210 under `/run`; only its public key was installed into LXC4000. Controller-to-guest SSH with `StrictHostKeyChecking=yes`, a dedicated `HostKeyAlias`, and the pre-pinned host key returned `P28_SSH_PINNED_OK`. The host-key fingerprint also remained unchanged after guest reboot. No Codex credential was provisioned.
- LXC4000 is now stopped. Temporary `/run/p28-session-lxc-4000` on PVE still contains the archive and test host-key material, and `/run/p28-session-4000` inside LXC210 still contains the temporary client key and known_hosts file. Do not mark cleanup complete until these exact temporary paths are removed; ask immediately before deletion. VMID 4000 is still allocated to the stopped test LXC.
- This proves the guest-level DHCP and cryptographically pinned SSH bootstrap is feasible without mutating frozen templates or using TOFU. It does not yet prove that the production Controller token can upload the generated archive, that the archive builder is implemented in the Controller, or that Codex CLI/auth and App Server bootstrap work. Task 4 is unblocked for implementation design but remains incomplete; Task 5 remains gated.

### Task 4 — live Controller-token permission check — 2026-09-30

- Read-only `GET /access/permissions?path=...` requests were made from LXC210 using the Controller's configured Proxmox API token; the token value was neither printed nor persisted. Effective privileges at `/`, `/nodes/William-ca-Waterloo-Router`, `/storage/local`, `/vms`, `/vms/4000`, and `/vms/4001` showed no granted bits among the relevant tested privileges, including `Datastore.AllocateTemplate`, `Datastore.AllocateSpace`, `VM.Allocate`, `VM.Config.*`, `VM.PowerMgmt`, and `Sys.Modify`. Existing read-only inventory/content GETs still return HTTP 200; read access does not establish write authorization.
- The PVE node's own read-only `pvesh usage` confirms its upload endpoint accepts `import`, `iso`, and `vztmpl`. The LXC create endpoint accepts `ostemplate`, `rootfs`, `net*`, `mp*`, `features`, `unprivileged`, and `ssh-public-keys`; the current test LXC remains `stopped`.
- Upstream PVE LXC API source specifies create permissions: `VM.Allocate` on `/vms/{vmid}` (or a pool), `Datastore.AllocateSpace` on storage, and `Sys.Modify` on `/` for privileged containers. The upload API requires `Datastore.AllocateTemplate` on the storage. The configured Controller token therefore cannot execute the proposed upload/create path today. No write endpoint was probed and no ACL was changed.
- Additional bootstrap constraint: PVE's `unmanaged` setup plugin has an empty `post_create_hook`, so although `ssh-public-keys` is accepted by the create schema, that hook will not install the key for `ostype=unmanaged`. The proven spike instead embedded the guest authorized key and pinned host key in its temporary archive. Production archive construction must preserve that trusted-host-key property or use a separately verified key-install path.
- **Current gate:** before live Controller lifecycle can pass, the project needs an explicitly reviewed least-privilege PVE permission path (including upload, privileged create, both SSD/HDD storage allocations, configuration/start/stop, and bridge use as applicable), plus a production archive builder and an end-to-end proof. There is a policy conflict to resolve: PVE's privileged-create API requires `Sys.Modify` on `/`, which is broader than a Session-VMID-scoped ACL. Do not grant this broad privilege implicitly. Preserve the user's privileged-Codex-LXC rule while evaluating alternatives such as a prebuilt immutable privileged template plus clone permissions, or a narrow PVE-side broker; changing to unprivileged containers would require a separate explicit decision. Do not broaden the existing token or change ACLs as part of read-only verification. LXC4000 and the two temporary `/run` directories remain untouched pending an explicit cleanup approval.
