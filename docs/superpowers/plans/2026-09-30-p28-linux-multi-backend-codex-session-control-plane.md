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
- The same token does have a broad worker-pool role at `/pool/codex-workers` (including `VM.Allocate`, `VM.Clone`, `VM.Config.*`, `VM.PowerMgmt`, and storage-related privileges), but those rights do not appear at `/vms`, the Session target paths, or the local/pool storage paths queried above. Do not treat worker-pool authority as Session authorization or place persistent Sessions in the worker pool without a separate isolation review.
- The PVE node's own read-only `pvesh usage` confirms its upload endpoint accepts `import`, `iso`, and `vztmpl`. The LXC create endpoint accepts `ostemplate`, `rootfs`, `net*`, `mp*`, `features`, `unprivileged`, and `ssh-public-keys`; the current test LXC remains `stopped`.
- Upstream PVE LXC API source specifies create permissions: `VM.Allocate` on `/vms/{vmid}` (or a pool), `Datastore.AllocateSpace` on storage, and `Sys.Modify` on `/` for privileged containers. The upload API requires `Datastore.AllocateTemplate` on the storage. The configured Controller token therefore cannot execute the proposed upload/create path today. No write endpoint was probed and no ACL was changed.
- Additional bootstrap constraint: PVE's `unmanaged` setup plugin has an empty `post_create_hook`, so although `ssh-public-keys` is accepted by the create schema, that hook will not install the key for `ostype=unmanaged`. The proven spike instead embedded the guest authorized key and pinned host key in its temporary archive. Production archive construction must preserve that trusted-host-key property or use a separately verified key-install path.
- **Current gate:** a prebuilt immutable privileged template plus clone path can avoid the fresh-create `/` `Sys.Modify` check, but requires narrow clone/config/storage/bridge/power/console permissions, a Codex-ready template, and a trusted bootstrap path that never reuses template host keys. The alternative rootfs upload/create path still requires broad `/` `Sys.Modify` for privileged creation and upload/storage rights. Do not grant broad privileges implicitly. Preserve the user's privileged-Codex-LXC rule while evaluating template clone plus a PVE-console bootstrap or a narrow PVE-side broker; changing to unprivileged containers requires a separate explicit decision. Do not change ACLs as part of research. LXC4000 and the two temporary `/run` directories remain untouched pending an explicit cleanup approval.

### Task 4 — privileged-template clone permission and identity review

- Current upstream PVE LXC clone API source confirms the authorization model: `VM.Clone` on `/vms/{template-vmid}`, `VM.Allocate` on `/vms/{new-vmid}` or the specified target pool, `Datastore.AllocateSpace` on each used storage, and `SDN.Use` on the bridge. Unlike fresh privileged-container creation, clone has no `/` `Sys.Modify` check. A dedicated Session pool plus source-template permission is therefore a plausible narrower capacity path, subject to storage/bridge grants and live effective-permission validation. Do not reuse `/pool/codex-workers`.
- Clone is not by itself a complete bootstrap: the clone endpoint accepts no `ssh-public-keys` field. Its post-clone path invokes the OS plugin's `post_clone_hook`; the normal Linux base hook clears machine-id, while SSH host-key regeneration belongs to `post_create_hook`. A cloned template therefore retains its source SSH host keys unless another verified mechanism rotates them before SSH becomes reachable. Do not use shared template host private keys or trust an unpinned first SSH connection.
- PVE exposes an LXC `termproxy` endpoint guarded by `VM.Console`. The official `pve-xtermjs` protocol is small: client input uses `0:<byte-length>:<text>`, resize uses `1:<cols>:<rows>:`, ping uses `2`, and server output is raw terminal bytes; the WebSocket negotiates the `binary` subprotocol and begins with `user@realm:ticket\n`. PVE changelog records API-token support for termproxy/vncwebsocket beginning in 9.0.13. A fresh `pveversion` check corrected the earlier version note: the installed host is `pve-manager/9.2.20/49318c671b82f31e`, kernel `7.0.14-14-pve`; its actual handshake has not been exercised. Installed `/usr/share/perl5/PVE/LXC.pm` confirms `cmode=shell` runs `lxc-attach --clear-env -n <vmid>`, avoiding the guest password-login prompt. This is a plausible server-side bootstrap channel for generating a per-instance host key and retrieving/pinning its public identity before enabling SSH. Still characterize bounded command/result framing with a real handshake. Console access is root-equivalent inside that guest and must remain server-side.
- Updated candidate: a prepared immutable privileged Session template (with Codex CLI/App Server but no account credentials), clone into a dedicated Session pool, then bootstrap identity and selected account state through a narrowly scoped, authenticated per-guest channel. Keep root disks on SSD `local`, workspace/data on HDD `pool`, and preserve DHCP for dynamic Session LXCs. The current frozen templates 3900–3902 are not candidates because inspection found they lack Codex CLI/App Server.
- Decision/ruling: do not switch the runtime adapter or expose Session creation yet. The clone permission result removes the fresh-create `Sys.Modify` obstacle, but template readiness and trusted per-instance bootstrap remain unproven. Next safe work is a read-only inspection of PVE termproxy/websocket behavior and a protocol-only harness design; no ACL, template, or guest mutation is authorized by this finding.
- References: [PVE LXC clone API source](https://github.com/proxmox/pve-container/blob/master/src/PVE/API2/LXC.pm), [PVE LXC setup hooks](https://github.com/proxmox/pve-container/blob/master/src/PVE/LXC/Setup/Base.pm), [PVE xterm.js protocol](https://github.com/proxmox/pve-xtermjs), [PVE API-token terminal support changelog](https://github.com/proxmox/pve-manager/blob/master/debian/changelog), [PVE LXC termproxy permission/API introduction](https://lists.proxmox.com/pipermail/pve-devel/2017-December/029826.html), [PVE privilege and resource-pool model](https://github.com/proxmox/pve-docs/blob/master/pveum.adoc).

### Task 4 — readiness safety and approved console spike continuation

- Added a mandatory read-only `Runtime.CheckReady` boundary before every `READY` promotion: create, resume, and reconciliation of ambiguous create/start/stop. Regression tests first reproduced premature readiness; the manager now retains `UNKNOWN` and its pending operation when readiness is not established, and later reconciliation does not replay clone/start/stop. The Proxmox implementation intentionally returns `ErrRuntimeNotReady` until guest bootstrap, the selected identity, and Codex/App Server readiness are configured and verified. A root console result is not a substitute for that proof.
- Strengthened `VerifyIdentity` to read back the actual root disk and require `local:` SSD storage, not merely trust the clone request's storage parameter. Tests cover absent/HDD/bind-path/wrong-storage root disks, and separate version/Session/epoch/generation ownership mismatches. Existing pool workspace preservation and worker boundaries remain unchanged.
- The isolated console adapter uses the existing `golang.org/x/net/websocket` dependency: exact Session binding and privileged/SSD checks, running-state and `cmode=shell` checks before one termproxy POST, then the authenticated `binary` WebSocket. It exposes only a fixed read-only UID/OS/hostname probe, never an arbitrary shell execution API. Console success is not wired into `CheckReady` and does not enable the Session API.
- User explicitly approved backing up LXC4000, setting only `cmode=shell`, and starting it for read-only console diagnosis. Backup: `/run/p28-console-4000.LyBECh/4000.conf.before`. Its initial post-start status-print command failed because Windows stdin added a trailing CR to `--output-format json`; a separate read-only query verified the resulting safe checkpoint: `running`, `cmode=shell`, unchanged hostname/rootfs/DHCP/privileged metadata. No hostname/description, account credential, template, ACL, or production service was changed.
- Live PVE-root termproxy TCP authentication and the fixed read-only command were verified. Terminal output exposed repeated CR before LF; normalizing trailing CR resolved the apparent command timeout. Guest result is UID `0`, OS `Linux`, hostname `localhost`; configured hostname remains `codex-session-4000-test`. The unmanaged-rootfs spike does not apply the normal OS hostname setup, so its guest identity does not match its config. Record transport as verified, not Session identity/readiness. This does not prove production Controller-token permissions or the live WebSocket path.
- Current LXC4000 state supersedes the earlier stopped snapshot: it is **running**, still without Codex account credentials. Its existing test SSH keys/archive, the new configuration backup, and subsequent test artifacts are retained; cleanup or further config replacement needs precise authorization. The production Controller token still lacks Session-specific grants; no ACL was widened. Task 4 and Task 5 live acceptance remain open.
- Final source regression: full Windows `go test ./... -count=1` and `go vet ./...` pass; Session runtime repeated five times passes. A frozen-source Linux/amd64 test binary with SHA-256 `bdde7eba08831dc3f30d7c05a08bb41e51ba3cc3af9db763f209fc7d88684b55` passed the complete Session runtime suite three times on PVE. Tests use simulated HTTP/WebSocket servers and a disposable SQLite database, not production endpoints.
- Linux test-environment findings: `/run` is intentionally `noexec`; do not relax it. The first Linux run also found the system `/tmp` tmpfs full (32 GiB), causing SQLite `database or disk is full (13)` while SSD root had approximately 320 GiB free. No database repair, mount change, or unknown-file cleanup was performed. Final tests used only an isolated per-process `TMPDIR` at `/var/tmp/p28-console-regression.95l0Hq/tmp.heglHt`; all SQLite/readiness/console tests then passed. An earlier binary built during concurrent source edits is superseded by the frozen-source hash above.
- Scoped reviews found false-readiness prevention addressed and no blocking console-source defect. WebSocket output/auth fragmentation across messages is tested; fragmentation of a single WebSocket message across protocol frames remains an explicit untested edge. These deterministic passes do not close live Controller-token WebSocket, per-instance SSH bootstrap, account identity, or Codex App Server readiness gates.
- Source checkpoint committed as `937c888` (`feat(sessionruntime): add bounded console probes and readiness gates`); no production binary or configuration was deployed. Unrelated pre-existing Session UI modifications remain untouched.

### Task 4 — real HTTP/WebSocket schema and permission isolation follow-up

- Subsequent fixed-target probes on PVE verified the real HTTPS termproxy POST and `binary` WSS path for LXC4000 using a PVE-root ticket generated only in remote process memory. TLS used `/etc/pve/pve-root-ca.pem` and certificate hostname verification for `10.58.2.187`; no TLS bypass, account secret copy, ACL change, or guest write occurred. The same fixed UID/OS/hostname command completed. This supersedes the earlier unverified root WebSocket transport note, but **does not establish Controller-token authority or matching Session identity**; guest hostname remains `localhost`.
- Actual HTTP `/api2/json/.../termproxy` returns `data.port` as a JSON string such as `"5900"` on the installed PVE version, despite the documented integer schema. The initial diagnostic rejected this shape; normalizing it proved the root WebSocket path. The production adapter needs a tested integer-or-decimal-string decoder with an explicit 5900–5999 port bound and rejection of malformed values. Sanitized traces record only the port type/value and result, never either ticket.
- Session clone requests must explicitly include `pool=codex-sessions`. The live `/pools` inventory currently contains `codex-workers`, `pool`, and `uq-pilot`, not `codex-sessions`. Source-side pool selection is being integrated separately; no pool was created and no existing instance was moved.
- Permission-isolation conflict found by read-only inspection: Controller uses `codex-controller@pve!controller-v3`, with `privsep=1`, but the same user also has an older `controller` token with `privsep=0`. Adding Session privileges to that user would implicitly expand the older token. Do not silently broaden the parent user or revoke legacy tokens. A distinct `codex-sessions@pve` identity/token with narrowly scoped Session grants, injected through the existing LXC210 secure environment mechanism, has been proposed for explicit approval; original worker credentials remain unchanged. No such identity/token/ACL has been created yet.

### Task 4 — approved independent Session identity and source integration

- User explicitly approved `codex-sessions@pve` and a minimum-permission separate Session token. Created `codex-sessions` resource pool, the new user, and `session-v1` with `privsep=1`; no password or root-path permission was added. Effective token permissions were verified over HTTPS with the PVE CA and hostname validation: `/pool/codex-sessions` has only `VM.Audit`, `VM.Allocate`, `VM.PowerMgmt`, `VM.Console`, `VM.Config.Disk`, `VM.Config.Network`, and `VM.Config.Options`; `/vms/3900` has `VM.Audit`/`VM.Clone`; `/storage/local` and `/storage/pool` have `Datastore.Audit`/`Datastore.AllocateSpace`; `/sdn/zones/localnetwork/vmbr0` has `SDN.Use`; `/` is empty. A token-authenticated template-config GET returned HTTP 200. No `Pool.Audit`, storage-upload permission, or global VM/node grant was added.
- PVE authorization configuration was backed up in root-only `/run/p28-session-identity.aadjrjdc` before creation. The secure LXC210 environment file was backed up as `/etc/codex-controller/proxmox.env.pre-p28-session-o_1yogbt`, then only `SESSION_PROXMOX_TOKEN` was appended through remote process stdin and an atomic private-file replacement. The existing environment fields were preserved byte-for-byte and final ownership/mode verified as root/0600. Token values never entered Windows, source, fixtures, browser state, or tool output. Existing ACL entries were compared before/after and remained identical; original worker user/tokens were not modified.
- A follow-up read-only check executed inside LXC210 loaded the secret from that existing private file and verified TLS using the PVE CA supplied through process stdin. The template GET returned 200, root permissions were empty, and an unowned LXC4000 config GET returned 403. Existing environment fields parsed identically to the backup. This proves the bearer/TLS boundary from the Controller guest; it does not prove production Go transport CA configuration or an authorized live console handshake.
- A first combined upload/preview command was rejected before execution by automatic permission review, which also flagged an extra proposed `Pool.Audit`. That item was removed; subsequent read-only checks verified no changes from the rejected command. Actual provisioning then ran as a separately identified, approved mutation with the exact narrower grants above. No bypass or automatic rollback was used.
- Source now supplies `pool=codex-sessions` in Session clone requests and requires a distinct `SESSION_PROXMOX_TOKEN` only when the optional Session runtime is enabled. Staging that token alone does not enable runtime creation. Worker `PROXMOX_TOKEN`, routing, task routes, and defaults remain unchanged; no fallback to worker credentials is allowed. This supersedes the earlier shared-credential construction note. Production Controller has not been rebuilt/deployed/restarted and Session routes remain unavailable.
- The termproxy decoder now accepts an integer or digit-only decimal-string port, restricted to 5900–5999. Tests reject missing/null/boolean/fraction/exponent/nondecimal/out-of-range inputs before WebSocket dial, without exposing ticket/token values. The live string-port regression failed before the fix and passed after it.
- Combined Windows `go test ./... -count=1`, `go vet ./...`, and `git diff --check` passed. A new frozen Linux Session-runtime test binary, SHA-256 `f1e03031342040c6967c91694c25c39231f3876986d7c8652f20b2d2ce184456`, passed the complete suite three times on PVE using only the existing isolated test `TMPDIR`. Initial execution lacked the copied file's execute bit; setting that bit on this one test artifact resolved it without relaxing mount protection. Mock negative TLS tests intentionally generate handshake errors; the suite exit status is zero.
- The isolated Linux Controller token-wiring suite also passed three times; binary SHA-256 `ed201ef98ec8807cde28c4519619f2186f9708c293e14002452f54b0d5a93073`. This snapshot predates the subsequent global-VMID availability fix below and is not evidence for that fix. Credential-only static review found no blocking defect; missing/reused Session credentials follow the existing fail-closed Controller configuration policy rather than silently using worker credentials.
- Task 4 is still incomplete: independent credentials and deterministic protocol tests do not prove a Session-token live console, per-instance bootstrap/SSH identity, account selection, App Server readiness, or full lifecycle. LXC4000 remains a credential-free diagnostic guest with a hostname mismatch, not a ready Session. Adding it to the new Session pool is a separate scoped operation requiring approval; no existing guest has been moved by this provisioning step. Frozen templates remain unchanged and still lack Codex CLI/App Server. Task 5 and automatic failover remain gated.

### Task 4 — global VMID collision check under least privilege

- Read-only live tests from LXC210 proved `/cluster/resources?type=vm` hides the existing, pool-external LXC4000 from the new Session token. Treating absence from that permission-filtered inventory as global availability was incorrect. The installed PVE `Cluster.pm` confirms `/cluster/nextid?vmid=...` is available to authenticated users and checks the unfiltered global VM list. Live requests returned HTTP 400 with `errors.vmid="VM 4000 already exists"` for occupied 4000 and HTTP 200 for free 4001; no VM was created or changed.
- Regression tests first reproduced `TargetAvailable(4000)=true` and `Observe(4000)=MISSING` after a failed guest-status read despite the hidden occupied guest. Session availability now uses the global assertion; only an exact structured 400 collision for the requested ID returns occupied. Successful integer/decimal-string IDs must exactly match the request. Authentication, authorization, generic 400, wrong collision ID, malformed payload, 5xx, and unexpected success IDs are errors, never invented availability. Failed observations of an occupied/uncertain guest remain UNKNOWN.
- This check does not eliminate the time-of-check/time-of-use race. SQLite reservation and PVE's clone collision check remain authoritative at mutation time; no automatic retry, deletion, VMID reassignment, or privilege broadening was added. Worker capacity code is unchanged.
- A fixed read-only probe using the actual production Go adapter ran on PVE with the Session credential passed only through process stdin. It correctly reported occupied 4000 unavailable, free 4001 available, worker-range rejection, and unowned 4000 observation UNKNOWN. TLS certificate and hostname were verified. This tested the global-check snapshot before the strict whole-JSON follow-up, not a production deployment.
- Review identified that the shared decoder accepted the first JSON object without rejecting trailing garbage, a second object, or an over-limit response. Three added tests reproduced that unsafe acceptance. Responses with an expected output are now read with a 1 MiB + 1 byte bound, reject bodies over 1 MiB, and use strict whole-document JSON decoding. A malformed clone acknowledgment remains `ErrOutcomeUnknown` after exactly one clone POST, with no follow-up mutation or replay. No production endpoint was fault-injected.
- After the final fix, combined `go test ./... -count=1`, `go vet ./...`, and `git diff --check` passed. A separate reviewer reran the complete Session-runtime suite and reported no remaining blocking findings. Final frozen Linux Session-runtime and Controller suites each passed three times after SHA-256 verification: `6b5c858e5b4735de8170ea86d253c045401da71c63ce56b6189c5f31e7dd3dd1` and `4c18dd793d92034b9afb97311afd3b30f469bda0fe88a2c38f92231ec1b3be2b`, respectively. The rebuilt production-adapter read-only live probe (`6d412b3c8dbec4615dc73c4791490a3c70f1366f1bbfc8fad43177152681c784`) again verified occupied/free/range/UNKNOWN behavior against PVE. No production binary was installed, no guest was modified, and no service was restarted.
- Source checkpoint commits: `62b83a6` (`fix(sessions): isolate PVE credentials and global VMID checks`) and `c197416` (`fix(sessionruntime): decode bounded string termproxy ports`). Only the nine scoped source/test files were staged; unrelated Windows Session UI changes remain untouched. These commits close this credential/availability/protocol slice, not Task 4 or Task 5 acceptance. The scoped LXC4000 pool-membership approval is still pending at this checkpoint.

### Task 4 — approved test-pool membership and Session-token console proof — 2026-10-01

- User explicitly confirmed adding only the credential-free diagnostic LXC4000 to `codex-sessions`, after backup, followed by the fixed read-only console probe. Preflight verified the pool was empty and this exact running privileged guest was not in another pool. Used `allow-move=0`, so the operation could not silently remove a guest from another pool. Backup: root-only `/run/p28-session-pool-4000.lo5yzsh7` contains the pre-change guest config, authorization config, and pool snapshot.
- Pool readback showed exactly LXC4000. Its `/etc/pve/lxc/4000.conf` remained byte-for-byte unchanged, SHA-256 `48845fa1220a52d51b75b704f46f0b3879e12b978377863ca24718a8ef8b8a56`; it remained running. No hostname/description, account, template, guest file, production service, or broader ACL was modified. No restart or deletion occurred. This supersedes the prior empty-pool/pool-external 4000 snapshot.
- The fixed-target diagnostic, run on PVE with the Session token read privately from the existing LXC210 environment file, verified HTTPS config/status access, termproxy HTTP 200, the exact token principal, and the real string-port schema. The binary WebSocket with `Authorization` header authenticated successfully using that termproxy ticket. TLS used the PVE CA and hostname validation; token/ticket remained only in remote process memory and were never printed or persisted. Diagnostic binary SHA-256 `b4393f67045dde508f0b0f1849fa3aee55a4c40f6eb43966c03115e438e0d2dd` was verified before execution.
- Fixed command result: UID `0`, OS `Linux`, hostname `localhost`, `guest_identity_matches=false`. This closes the scoped API-token console transport feasibility gate, not the production `ProbeConsole` identity gate or real Controller-process integration. LXC4000 is still a diagnostic spike, not a Store-bound Session; its missing ownership metadata and known hostname mismatch were not repaired or bypassed.
- Earlier negative `GET /lxc/4000/config=403` and unowned-4000 UNKNOWN probes are historical pool-external observations, not the expected current result after approved membership. The token now intentionally has its confirmed Session-pool authority over this diagnostic guest; its global/root privileges remain unchanged.
- Post-membership read-only checks verified root permissions are still empty, access to protected LXC210 config returns 403, and every original worker/other-user ACL entry still matches the pre-Session snapshot. No ACL was broadened by the membership operation.
- **Remaining Task 4/5 gates:** design/verify bootstrap in a new clone without mutating frozen templates: per-instance SSH host-key rotation before SSH exposure, Controller-owned per-instance client key and cryptographic pinning, verified Codex installation, selected-account provisioning/identity, and App Server readiness. Current approval did not authorize credential injection or further config changes to LXC4000. Keep READY fail-closed, Session API unavailable, and automatic failover disabled; do not infer full lifecycle acceptance from this transport proof. All test artifacts and backups remain retained pending exact cleanup approval.
- Proposed next scoped bootstrap spike, **not executed/approved by this confirmation**: a new credential-free clone of 3900 in the Session range/pool, with root on `local`, workspace on `pool`, and network fenced during initial SSH identity setup. Use the limited console channel for non-secret per-instance host-key rotation and client-public-key installation, then cryptographically pin the SSH host identity before enabling guest connectivity and installing a verified Codex artifact. Do not copy template private keys, Windows SSH private keys, or account credentials; do not alter LXC4000, frozen templates, worker/control services, or expose a Session API. Verify actual template services and network-fencing API semantics before implementing commands; inspect/remove only clone-local worker startup behavior if required, never assume the service inventory. This is the next bootstrap security boundary to approve, not proof of readiness.

### Task 4 — approved new-clone SSH/Codex bootstrap spike — 2026-10-01

- Subsequent user confirmation authorized this exact new-clone spike. Session-token clone3900→4001 completed once; root is SSD `local`, workspace is separate 8 GiB ZFS `pool`, and initial `link_down=1` was verified as a DOWN/non-bridged host veth with zero guest traffic. Template3900 and existing4000 config hashes remained unchanged.
- Inspected the clone's inherited `k3s-agent`, stopped/disabled/masked only that clone service before network exposure. Generated a fresh guest host identity and LXC210-owned per-instance client identity; console verified the public host key and wrote the Controller-side pin before enabling DHCP. Strict SSH from LXC210 passed; missing pin/unknown alias was rejected. No Windows private key, account home or provider auth was copied.
- Installed only the existing approved Codex CLI0.155.0 binary from3006, matching its documented SHA-256 through transfer and installation. Through LXC210 pinned SSH, App Server `initialize`/`initialized`, unauthenticated `account/read`, and empty `thread/list` passed with stdin held open. No login, model turn, production service restart or Session API enablement occurred; App Server probe exited and no Codex process remained.
- Safe checkpoints handled the PVE description newline and missing `/run/sshd` after stopping SSH without replaying clone/key rotation. The initial read-only console timeout's cause is unresolved. Detailed sanitized evidence, retained sensitive backup paths and source regression are recorded in [the bootstrap report](../../migration/p28-session-bootstrap-2026-10-01.md).
- **New explicit pre-account gate:** the clone still contains inherited worker environment fields `K3S_TOKEN`/`K3S_URL` and old shared SSH private keys/backups. They were not printed or deleted. A full-access real Session must not receive this filesystem before an exact credential inventory and authorized clone-only sanitization (or an approved clean Session image). Masking K3s is not credential isolation. Do not inject Codex account auth or run a model in4001 yet.
- This closes the scoped credential-free Codex bootstrap feasibility test, not Task4 production lifecycle, selected-account readiness, reboot/mismatched-key tests or Task5. Production bootstrap code remains unimplemented; scratch helpers require unconditional safety gates and protocol/security tests before reuse. `CheckReady` remains fail-closed, automatic routing disabled and Session API unavailable. LXC4001 is retained running as a diagnostic spike; no cleanup is authorized.

### Task 4 — approved4001 inherited-credential cleanup — 2026-10-01

- Subsequent exact user confirmation authorized backup on PVE followed by deletion of the ten specified clone-local K3s/old-SSH targets. Fresh preview found no target symlinks or nested mounts. A root/0700 PVE directory `/var/tmp/p28-sanitize-4001-svgxne67` holds the root/0600 sensitive tar and manifest; 34,227 members were checked against the approved roots before deletion. No secret entered Windows/logs and no automatic retry/replay occurred.
- Deleted only4001's `/etc/rancher`, `/var/lib/rancher/k3s`, worker environment file, guest-local bootstrap backup, and six old SSH host-key/private-public files. Verified targets absent; new P28 SSH keys/config, authorized client key, Codex hash and workspace device/inode unchanged.3900/4000/4001 PVE configs stayed byte-identical; no template, other guest, volume, ACL or production control service was modified or destroyed.
- Fresh LXC210 pinned SSH/missing-pin checks and unauthenticated App Server initialize/account-null/empty-history checks passed after cleanup. Removed material remains recoverable from the external sensitive backup, not from the future Agent filesystem. Recovery extraction was not rehearsed. See [sanitation evidence](../../migration/p28-session-bootstrap-2026-10-01.md#approved-clone-only-sanitation--2026-10-01).
- A separate real wrong-host-key fixture (public key only) on210 was rejected with SSH exit255/host-identification-changed; correct known_hosts/client key was untouched. This closes mismatched-key rejection, not reboot/pin persistence. Fixture retained; no further cleanup was inferred.
- The prior known-secret stop gate is closed for these exact paths; whole-image credential/provenance audit, reviewed production bootstrap/readiness, restart verification, account identity and full persistent lifecycle are still open. Do not treat filesystem deletion as secure physical erasure or mark Task4/5 complete. No account injection, model turn, READY, Session API or automatic routing was enabled.

### Task 4 — source bootstrap isolation guard — 2026-10-01

- Clone configuration now requests DHCP with `link_down=1` on every inherited network interface, `onboot=0`, and `cmode=shell` before the manager starts the clone. Readback must match the requested isolation configuration and contain no unobserved network interface. A failed post-mutation proof returns `ErrOutcomeUnknown`; configuration is not retried. This is a source guard, not a production deployment or complete bootstrap implementation.
- Ownership parsing explicitly accepts a single terminal LF/CRLF supplied by PVE and rejects interior or repeated line endings. Correction to the initial investigation: Go's Base64 decoder already ignored CR/LF, so terminal newline rejection was in the scratch spike helper, not this production decoder. The new production guard tightens malformed metadata acceptance without changing the ownership binding.
- TDD regressions first failed for absent fencing and overly permissive ownership line endings; an added readback fault test also failed for a newly appearing NIC before the guard was fixed. Fresh Windows `go test ./... -count=1` and `go vet ./...` passed. Go telemetry still reports an access-denied warning; no cache/ACL changes were made. An earlier scoped command named nonexistent `internal/controller` and failed setup; the full run covered actual `cmd/controller` successfully.
- Bootstrap contract review confirms the next insertion point is after starting the fenced, owned clone and before read-only readiness. Durable generation-bound intent/completion checkpoints, trusted console-derived per-instance SSH pinning, sanitation/artifact verification and read-only reconciliation are required. Do not put bootstrap side effects inside `CheckReady`, treat the scratch4001 script as production, or provision account credentials before the remaining image/readiness gates. Task4 remains in progress and Task5/API/automatic routing remain gated.
- After SCP completed, Linux binary hash `a7695b804be472fb394216d7cd13817e67c054cc9a984481d06c518117857081` matched and the complete Session runtime suite passed three consecutive runs on PVE in the existing isolated test directory. These tests use mocked upstreams, not actual guest lifecycle operations. Expected negative TLS fixture diagnostics occurred without test failure. An earlier premature checksum while SCP was still writing failed; no test executed then, no transfer was interrupted/replayed, and the completed-file check supersedes it. The test artifact is retained at `/var/tmp/p28-console-regression.95l0Hq/sessionruntime-fence-20261001.test`.

### Task 4 — restart and durable checkpoint slice — 2026-10-01

- Exact diagnostic4001 reboot submitted once with external private intent checkpoint; new startup observed, keys/pin/workspace and3900/4000/4001 config hashes unchanged. Post-restart strict SSH and credential-free App Server initialize/account-null/empty-history passed. See [restart evidence](../../migration/p28-session-bootstrap-2026-10-01.md#diagnostic-reboot-and-source-account-baseline--2026-10-01). This is not authenticated Session restart or production Controller integration.
- Codex A baseline explicitly selected as LXC3006's current account; source-only Linux `account/read` reports ChatGPT Plus. No credentials copied, no B use. Stable account/workspace matching and authenticated model turn remain pending; email or plan label alone is insufficient.
- New source persistence/coordinator slice uses seven ordered generation-bound checkpoints, fresh claim before Apply, read-only observation of INTENT/UNKNOWN and separate runtime/account readiness. Optional Manager bootstrap applies only during new creation; resume/reconcile never replay bootstrap. No real production driver, existing DB migration, binary deployment, READY promotion or API enablement is implied. Review and combined regression evidence must be recorded before committing this slice.
- Original V3 attachment is reconciled in [the current execution overlay](2026-10-01-v3-reconciled-execution.md); stale pool/model/Windows/rebuild assumptions do not supersede later user decisions. Task4 and authenticated Task5 remain open.
- Final independent review cleared Store/coordinator/Manager slices after migrations9–11 rejected evidence mutation and SQLite REPLACE bypass. Fresh full Go tests/vet/diffcheck passed; frozen Linux Store and Session-runtime suites each passed three times after completed-transfer SHA verification. Detailed hashes and limitations are in [source evidence](../../migration/p28-session-bootstrap-2026-10-01.md#reviewed-durable-bootstrap-source-slice). This closes only the durable source slice, not the production driver, live deployment or account gate.

### Task4 — SSH material, transport and isolation source continuation

- Generation-bound SSH material and pinned Linux App Server transport pass frozen Linux tests; hashes and limitations are recorded in [the source-validation report](../../migration/p28-session-bootstrap-2026-10-01.md#ssh-material-and-app-server-transport-source-validation). Independent SSH review approved the Linux scope after an explicit non-Linux launch rejection was added. No live account credential was transferred.

- Fixed authenticated console host-key observation is implemented and reviewed. It canonicalizes actual OpenSSH public-key comments, rejects unsafe/malformed output and rechecks the selected isolation digest before returning. Final frozen Linux mock tests passed three times; full Go tests/vet pass. See [console evidence](../../migration/p28-session-bootstrap-2026-10-01.md#reviewed-fenced-console-host-key-probe). It remains a prerequisite, not the connected production bootstrap driver or Task4 completion. Existing diagnostic4001 is not automatically adopted.

- The clone image audit confirmed4001 inherits template3900's default snakeoil TLS private identity. Exact backup/removal of4001's `/etc/ssl/private/ssl-cert-snakeoil.key` and `/etc/ssl/certs/ssl-cert-snakeoil.pem` is awaiting immediate user confirmation; the previous ten-path cleanup did not authorize these additional paths. No cleanup or account provisioning has been performed. Source HostPin integration proceeds independently; this live sanitation gate remains open.

- The private HostPin stage adapter now connects console-observed identity to exclusive pin creation and read-only reconciliation without repair. Independent review and combined frozen Linux HostPin/console/material tests (three runs) passed; full Go tests/vet pass. Detailed artifact and caller-claim limits are in [HostPin evidence](../../migration/p28-session-bootstrap-2026-10-01.md#hostpin-stage-integration-prerequisite). Controller production assembly still needs the complete bootstrap driver; Task4 and account gates remain open.
- New read-only `VerifyBootstrapIsolation` validates identity and every network fence in one PVE config snapshot, rejecting duplicate network options, extra mounts and wrong storage. Linux and source regressions pass; it is a prerequisite for the production driver, not permission for a future mutation or proof of packet isolation.
- Production console action integration, full image sanitation/provenance, artifact installation and selected-account readiness remain open. Keep Task5, Codex B login, Session API and automatic routing behind their gates.
- Fresh inventory confirms8007 HTTP200. An old K3s3017 Node remains NotReady although the corresponding LXC is absent; old P13 Job records still report Running. Preserve them pending Controller/Pod reconciliation; no destructive cleanup is inferred.
