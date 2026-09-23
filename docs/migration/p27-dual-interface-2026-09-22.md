# P27 dual interface deployment — 2026-09-22

## Scope

P27 adds two operator-facing paths without restoring the frozen WebCodex
architecture:

```text
ChatGPT -> Secure MCP Tunnel -> 127.0.0.1:8090 -> Controller :18080
browser -> private access path -> 127.0.0.1:8081 -> Controller :18080
```

The self-hosted UI is static React served by the private BFF. The MCP gateway
and BFF contain no scheduler, task database, model router, or Proxmox/K3s
executor. The Controller remains authoritative.

## Code milestones

- `6d9b47a` — dual-interface design and implementation plan.
- `0feb2d7` — private task UI shell, BFF, systemd unit, and deployment docs.
- `038a402` — task detail, attempt/message/event views, SSE reconnect, and
  guarded continuation/cancel/retry controls.
- `bbf2686` — attempts listing API regression test.
- `8aac30f` — optional authenticated MCP-to-Controller HTTP adapter.
- `4c0c455` — BFF static asset serving and permission test.
- `dc84c6d` — BFF systemd ordering corrected to `codex-controller-v3.service`.

## LXC210 deployment evidence

LXC210 `codex-control` was not stopped. The existing Controller remained
active on `0.0.0.0:18080` and returned `/v1/ready` HTTP 200. New services were
installed and enabled:

```text
codex-mcp-gateway.service  active  127.0.0.1:8090
codex-task-ui-bff.service  active  127.0.0.1:8081
```

The deployed inputs were hash-checked before installation:

```text
codex-mcp-gateway       0985633ca6acb248bc51ad1d40e0eeb4c10667a313989796356b8bcb524d2c76
codex-task-ui-bff       3aa8debf62d9ade5c98f51652734810ac9d10c77fd1d47350ab7253c101d918d
task UI assets           4fa4cbcda5b38c62065eb43e780deaef6d55049548bec2b554f873e40ab1d8d3
```

The exact binary hashes were verified on both Windows and LXC210. Env files are
mode `0640`, owned by root
and their respective service group. Tokens were generated on LXC210 and were
not printed, committed, or sent back to the client. The MCP gateway has the
minimum shared-group access needed for the authoritative SQLite database.

## Black-box verification

- static UI `GET /` through BFF: HTTP 200;
- unauthenticated BFF API request: HTTP 401;
- authenticated BFF `GET /ui/api/tasks`: HTTP 200 with existing task state;
- MCP authenticated `tools/list`: HTTP 200 and all expected tools, including
  `dispatch_task` and `continue_task`;
- MCP authenticated read-only `get_task`: HTTP 200;
- all three systemd services active after installation;
- no external listener was added; both new interfaces bind loopback only.

## Not yet production-accepted at the 2026-09-22 checkpoint

The following items were open at this checkpoint. The Secure MCP Tunnel item
was closed by the 2026-09-23 runtime evidence below; the remaining items are
still intentionally open.

- Secure MCP Tunnel had not yet been connected to a ChatGPT workspace;
- no live side-effecting MCP dispatch or continuation was issued during the
  deployment smoke test;
- no external HTTPS/Tailscale static UI proxy has been configured;
- a previous full Go run had an intermittent Windows/SQLite `TempDir RemoveAll`
  failure in `TestNewHandlerFromEnvironmentServesReadyWithCompleteConfig`;
  the connection-lifecycle fix now makes the repeated test stable.

These are explicit remaining gates, not claims of completed ChatGPT or
production coding-task E2E.

## 2026-09-22 VMID collision recovery

The first ChatGPT MCP dispatch reached the Controller but stopped before Job
creation because the SQLite capacity ledger selected VMID 3011 while a stale,
stopped LXC3011 existed in Proxmox outside the ledger. Proxmox returned:

```text
CT 3011 already exists on node 'William-ca-Waterloo-Router'
```

The stale LXC3011 was explicitly verified as a dedicated dynamic worker and
destroyed. Template LXC3013 was preserved. The failed attempt remains
UNKNOWN/unsuitable for replay; the next live test must use a new task and
idempotency key.

The Controller now has tests for both collision windows: a read-only Proxmox
target preflight skips VMIDs present outside SQLite, and a deterministic
`already exists` clone response discards only the uncreated claim and selects
the next candidate. Network failures, timeouts, and other ambiguous 5xx
responses still remain UNKNOWN. At the time of this checkpoint the updated
Controller binary was not yet deployed because the PVE host had become
unreachable over SSH; the deployment and live proof are recorded below.

## 2026-09-22 collision fix and Controller-dispatched GLM proof

The PVE SSH path recovered. The Controller capacity adapter was rebuilt and
deployed to LXC210 with a Proxmox inventory preflight and deterministic retry
for externally occupied VMIDs. The deployed Controller binary was verified
with `/v1/ready` and SHA256:

```text
466edd7cbd5eeb55417ce17ea1a8aefc5b16bb8d26f6baa16dc93fe2a3503c0
```

A live allocation selected VMID 3016 while 3011, 3012, 3014, and 3015 were
occupied or retained by prior evidence. This confirms that the allocator no
longer trusts SQLite alone for Proxmox VMID availability.

The first post-fix worker reached agentd after a per-node image import and
reported the next real defect:

```text
fatal: repository 'WilliamLi0623/codex-homelab' does not exist
```

The repository exists and the `codex-homelab-v3` branch is present on GitHub.
The worker was passing the API shorthand directly to `git clone`, where Git
interpreted `owner/repo` as a local path. `internal/workspace` now converts
that exact safe shorthand to `https://github.com/owner/repo.git`, while
preserving explicit HTTPS, SSH, and local sources. Unit tests cover both
normalization and preservation cases.

The fix was built into agentd SHA256
`0296fb99c322b294e03a7c8d30c6e5221fcbf434fd7c1d7122509224e6e45b54`, repacked
as:

```text
localhost/codex-worker:agentd-glm-chat-p27-repo-normalize-0296fb99
```

The Controller environment was backed up before switching to that image tag.
The worker image was imported into each disposable node used by the proof; the
long-term bootstrap-independent preload/registry gate remains open.

The complete Controller-dispatched proof then succeeded:

```text
task:    task-d86adff1ee4f88f9d5776cf745240963
attempt: attempt-34bf80b2b2069dc127fde66f9867fd9d
profile: glm-5.3-flash
reasoning effort: max
validation: PASSED
commit:   301f3ae57c2757c28e6e0a9865c6f9fdaf8fa51a
state:    SUCCEEDED
```

The probe created one temporary file inside the disposable checkout, ran the
validation command, produced the local commit, persisted the completion and
task lifecycle events, and did not push. The worker LXC was stopped by the
normal release path. The earlier failed probe remains evidence of the clone
and no-change failure path; its disposable LXC was not destroyed in this
checkpoint.

The relevant internal packages and command packages pass. The Windows SQLite
`TempDir RemoveAll` cleanup race in
`cmd/controller/TestNewHandlerFromEnvironmentServesReadyWithCompleteConfig`
was fixed by applying the busy timeout per connection and closing idle
connections; the test passes in a 20-run repetition.

## 2026-09-22 persistent worker-image bootstrap

The dynamic-worker image gate was closed without modifying the original
template 3013. A full clone of 3013 was created as VMID 3090, the verified
GLM worker image was imported into its K3s/containerd store, the temporary K3s
Node was removed, and VMID 3090 was converted to the preserved template
`codex-k3s-agent-template-glm`.

The Controller environment was backed up and switched to:

```text
PROXMOX_TEMPLATE_VMID=3900
KUBERNETES_WORKER_IMAGE=localhost/codex-worker:agentd-glm-chat-p27-repo-normalize-0296fb99
```

Templates 3004, 3005, 3013, and 3090 are privileged legacy/rollback resources.
The replacement worker-template migration targets are LXC3900–3902. None of
these legacy resources are deletion targets as part of this synchronization.

The new template was added to the existing `codex-workers` pool and received
the same narrow `CodexWorkerController` token ACL as the old template. A
Controller-dispatched task then cloned VMID 3020 from the new template and
started its Job without any per-node image import. The complete lifecycle
succeeded:

```text
task:    task-0cd059f989309644ac5419c10a8b9b76
attempt: attempt-978ef55612550beabb25701214ca3443
profile: glm-5.3-flash
validation: PASSED
commit:   332a1172146060c0cacdd2cda472766170c18732
state:    SUCCEEDED
```

The failed 403 attempt also exposed and fixed a ledger edge case: a
deterministically rejected Proxmox clone now removes its still-`CREATING`
claim, while UNKNOWN network/provider outcomes remain unreconciled. The
Controller binary containing that fix is deployed with SHA256
`82ceadd5df084a2594b331a65f1fc892588a7c05425a207ff88a6ff9550009a`.

The historical `CREATING` ledger row for VMID 3019 from the first
3090-permission rejection was removed after an exact pre-delete check and a
SQLite backup at
`/var/lib/codex-controller/controller.sqlite.pre-stale-claim-20260922.bak`.
The exact row was absent after the cleanup, `CREATING` count is zero, Proxmox
has no VMID 3019, and K3s has no corresponding Job, Pod, or Node. The
Controller was restarted and returned `/v1/ready` with `{"status":"ready"}`.
## 2026-09-22 live dual-interface and durable-release checkpoint

The current P27 working tree was validated without changing the existing
OpenAI path or the Controller/K3s architecture:

```text
frontend tests: 2 files, 6 tests passed
frontend build: Vite production build passed
Go packages: `go test ./... -count=1` passed
Controller: active, /v1/ready = {"status":"ready"}
MCP Gateway: active, 127.0.0.1:8090 only
UI BFF: active, 127.0.0.1:8081 only
UI BFF unauthenticated /ui/api/tasks: 401
UI BFF authenticated /ui/api/tasks: 200 application/json
MCP authenticated tools/list: 200
MCP authenticated get_task: 200
```

An authenticated MCP client completed one real guarded write through the
existing Controller. It created the task, started the `glm-5.3-flash` attempt,
dispatched it, validated the file change, and persisted a local commit without
pushing:

```text
task:       task-e0725def8d218bab95d65a9a7887fe42
attempt:    attempt-16dcc5deab01a9f4170f0e2f2e19d2cb
profile:    glm-5.3-flash
validation: PASSED
commit:     11410e6430278c5f7ef46ecbdc76eabb0903db88
task state: SUCCEEDED
```

The first `dispatch_task` HTTP client call exceeded the old 30-second Gateway
write timeout, but the request later completed. The request was not retried,
because retrying an ambiguous side-effecting dispatch could duplicate work.
`cmd/mcp-gateway` now uses a bounded two-minute write timeout, covered by a
test and deployed as Gateway SHA256
`75f70c7b8ec9e2cdc5b892188e288cb7b3e5a361b8984714d44590396ac77ee5`.

That live run exposed a durable cleanup-recovery gap. A completed attempt with
validation persisted but release progress stuck at `VERIFY_STOPPED` was
excluded from the Controller retry query, and after requeue the missing Job
was incorrectly treated as an unresolved worker observation. The fix has two
parts:

1. completed attempts with incomplete release progress re-enter the durable
   observation queue;
2. when validation and the local Git ref already exist, recovery retries only
   capacity release and never recollects or reruns the worker.

The fix is covered by regression tests, and the deployed Controller SHA256 is
`732008922e734a39dff13377f6a54e4555b55e85bd65f186854ecff6c5876bc8`.
The previous binary was backed up at
`/usr/local/bin/codex-controller-v3.pre-p27-durable-release-20260922`.
For VMID 3021, release progress is now `DONE/COMPLETED`; the PVE container
configuration, matching K3s Job/Pod, and matching K3s Node are absent. The
historical SQLite claim row remains `CLAIMED` for audit safety and was not
manually deleted; reclaiming historical rows is a separate capacity-ledger
change and is not silently folded into this checkpoint.

At this checkpoint the Secure MCP Tunnel was still not connected to a ChatGPT
workspace, so the MCP proof above was a private in-LXC authenticated client
proof. The later runtime evidence below supersedes that statement. Browser
visual smoke and cross-surface UI task creation/continuation/reconnect
acceptance also remain open because the BFF is intentionally loopback-only. No
public listener was added to bypass that boundary.

## 2026-09-23 capacity-claim reclaim live proof

The Controller was rebuilt and deployed with capacity-ledger reclamation. The
new behavior deletes a claim only after release progress reaches
`DONE/COMPLETED`; repeated release remains idempotent through the durable
release record, while UNKNOWN release outcomes remain protected.

The live GLM probe completed through the Controller path:

```text
task:       task-684a4ebb1243ea29dbebeada2bb41e8b
attempt:    attempt-a2f438c495493e093d548e13e277fc13
profile:    glm-5.3-flash
VMID:       3022
validation: PASSED
commit:     c8d68cbdcdcd16798c97c269366220b80e6727bc
task state: SUCCEEDED
release:    DONE/COMPLETED
```

The first release observation reached `VERIFY_STOPPED/UNKNOWN`; an exact
external reconciliation then verified PVE VMID 3022 stopped and its matching
K3s Job, Pod, and Node absent. The Controller resumed release, destroyed the
worker, and removed the new `capacity_nodes` claim. The PVE configuration for
3022 is absent. This proves the new claim-reclaim path without manually
deleting a production database row.

The deployed Controller SHA256 is
`b93eeeead065b021f17ac29c7d3dc803c9f048f4ee5a2ca558173ab1dfd55e86`; the
previous binary is backed up at
`/usr/local/bin/codex-controller-v3.pre-p27-capacity-reclaim-20260923`.
Historical claims from earlier runs, including previously UNKNOWN/CREATING
rows, were not broadly deleted and remain a separate explicit reconciliation
scope.

## 2026-09-23 historical-claim cleanup and Secure MCP Tunnel runtime

The explicitly approved cleanup was performed from an exact preview. The preview
contained 14 historical `CLAIMED`/`CREATING` rows for VMIDs
`3001,3002,3003,3007,3008,3009,3010,3015,3016,3017,3018,3019,3020,3021`.
A SQLite backup was created before deletion at:

```text
/var/lib/codex-controller/controller.sqlite.pre-delete-claimed-creating-20260923
```

Only the three stopped dynamic containers with no matching active K3s Job, Pod,
or Node were destroyed: LXC3015, LXC3018, and LXC3020. The other 11 historical
ledger rows were deleted from the SQLite claim table after the exact VMID check.
The post-check found zero rows for all 14 target VMIDs, four unrelated
`UNKNOWN` rows remained untouched, and the base templates were preserved.

The OpenAI Secure MCP Tunnel client v0.0.14 is installed in LXC210 and managed
by `codex-mcp-tunnel.service`. It uses the root-only environment file
`/etc/codex/tunnel-client.env`, the tracked profile
`deploy/tunnel-client/codex-homelab-mcp.yaml`, and the tracked unit
`deploy/systemd/codex-mcp-tunnel.service`. The tunnel client and MCP Gateway
both bind locally; the tunnel is outbound-only and no API key is stored in this
repository, logs, fixtures, or ChatGPT prompts.

Runtime evidence:

```text
codex-mcp-tunnel.service: active/enabled
healthz: HTTP 200 on 127.0.0.1:8091
readyz: HTTP 200 on 127.0.0.1:8091
MCP initialize: protocol 2025-06-18, server codex-controller v3
tunnel: tunnel_6ab230aeb7c88191b5d2cd83ff57783f
```

The existing installed ChatGPT App `Codex Homelab Controller LXC210 v2` was
used for a read-only smoke test and returned 20 tasks. Tunnel-client logs
recorded ChatGPT discovery traffic and a subsequent RPC request on the same
tunnel ID, proving the ChatGPT-to-tunnel-to-MCP path. Creating a new duplicate
App from the ChatGPT Plugins UI was attempted twice and both attempts failed
with the platform's generic `Error creating connector` message; this is an
external ChatGPT connector-creation blocker, not a local tunnel or gateway
failure. The existing App remains usable.

The gateway now returns an explicit HTTP 404 for OAuth discovery paths because
this private MCP endpoint intentionally does not advertise OAuth/DCR. This
lets tunnel-client classify the endpoint as a non-OAuth MCP target while the
authenticated MCP session continues to initialize normally.

OpenAI's reference for the tunnel association and runtime model is
[Secure MCP tunnels](https://developers.openai.com/api/docs/guides/secure-mcp-tunnels).
