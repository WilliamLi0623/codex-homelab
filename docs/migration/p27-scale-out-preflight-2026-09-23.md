# P27 scale-out preflight and live proof — 2026-09-23

This migration record preserves the initial memory-gated preflight and the later successful P27 live scale-out/scale-in test. P27 concurrency passed; the earlier VMID 3002 UNKNOWN claim remains fail-closed with no external worker resource observed.

## Initial preflight

The test did not start at this checkpoint because the Proxmox host had insufficient memory headroom for two workers at the configured template limit.

```text
PVE host memory:       62 GiB total, 57 GiB used, 5 GiB available
PVE host swap:         975 MiB used, 96 MiB free
LXC3900 worker limit:  4096 MiB, 2 cores
```

Two simultaneous workers could each use up to 4 GiB. Starting them with only 5 GiB available and almost no free swap would risk host memory pressure, so no concurrent worker was allocated.

## State preserved during the initial preflight

The preflight made no infrastructure changes. It did not delete or reconcile the four historical `UNKNOWN` capacity claims for VMIDs `3000`, `3011`, `3012`, and `3014`. It also left the old P13 Jobs and Pods untouched:

```text
codex-p13-e2e-20260920-r7  Pod: CreateContainerConfigError
codex-p13-e2e-20260920-r8  Pod: CreateContainerConfigError
```

No disposable worker was running after P26 cleanup. LXC3006, the fixed infrastructure guests, templates, and VM101 were not changed.

## Gate status after the initial preflight

At the initial preflight checkpoint, P27 scale-out/scale-in remained open. The completed P26 probes proved single-worker clone, execution, validation, local commit, restart recovery, and release; they did not prove concurrent scale-out. The later live proof below supersedes this checkpoint for P27 concurrency.

The P28 secret/isolation audit remains a later gate; this preflight did not run a complete Git-history secret scan or live cross-task isolation test.

## 2026-09-23 live concurrent scale-out proof

After the PVE host's Cisco VPN agent was gracefully stopped (the unit was not
disabled), available memory increased to approximately 17 GiB. Tailscale,
`rathole`, and the Controller remained healthy. The P27 test then exposed a
real clone race: concurrent dispatches both cloned template 3900, and Proxmox
returned `500 CT is locked (disk)` to one request. The rejected request had no
UPID, but the generic 5xx handling conservatively marked VMID 3002's claim
`UNKNOWN`. Read-only checks found no LXC config or matching K3s resources for
3002. That claim remains untouched rather than being manually deleted.

The fix was committed as `0d135ce` (`fix(capacity): serialize Proxmox template
clones`). A single Controller runtime now serializes template clone plus UPID
completion; only Proxmox's exact `CT is locked (disk)` response for a clone is
classified as a definite rejection. Other 5xx outcomes still fail closed as
`UNKNOWN`. The two regression tests passed 20 consecutive runs, and these
packages passed:

```text
go test ./internal/capacity ./internal/orchestrator ./internal/api ./cmd/controller
```

Controller `0d135ce` was deployed to LXC210 after backing up the previous
binary. The live binary SHA256 is
`c4ded51f577cb2a609db4b8b8ac72a11699b9208a33e72c921196ba5b48e2fae`; the
previous binary is preserved at
`/usr/local/bin/codex-controller-v3.pre-p27-clone-serialization-20260923`
(SHA256 `5076cf8dd3355fadfc8c84454be61707952b463433cb82749b7bd50889295c7a`).
The service restarted and `/v1/ready` returned ready.

Two new GLM 5.3 Flash tasks were dispatched concurrently. Their clone
operations were serialized, but their worker executions overlapped on separate
dynamic LXCs. At the same observation both Nodes were `Ready`, both Jobs and
Pods were `Running`, and each Pod was on its task's dedicated LXC:

| Task | Attempt | VMID | K3s node IP | Job/Pod overlap | Validation | Local commit |
| --- | --- | ---: | --- | --- | --- | --- |
| `task-8cc1e130ef7ff08a1586f22cc9cdee9c` | `attempt-af172af8ee84b68605a0c8db91cf9c18` | 3001 | `10.58.2.67` | Running | PASSED | `c020940263d31b785cbbd27c13537395fdb58816` |
| `task-040cdc915437f3d5ae9aca8a09322cb6` | `attempt-6b6835c116d01e824312a0310c1d5aff` | 3003 | `10.58.2.68` | Running | PASSED | `6f4880709d539f52139f3efaa4d4a283ad76b09b` |

Both tasks reached `SUCCEEDED`; each validation verified its own distinct file.
After exact external checks showed each LXC stopped and its K3s Node, Job, and
Pod absent, both release records were reconciled and reached `DONE/COMPLETED`.
The two P27 capacity claims were reclaimed; neither LXC configuration remains.
Host available memory returned to approximately 17 GiB. The earlier VMID 3002
`UNKNOWN` claim remains a fail-closed ledger entry, with no external worker
resource observed.

P27 scale-out/scale-in acceptance is now demonstrated for two simultaneous
tasks. P28's secret/isolation audit and later phase gates remain open.

## 2026-09-23 updated GLM worker image and Controller-dispatched proof

The Muse Chat adapter now identifies itself to CC Hub as a coding agent using
the `codex_cli_rs/<version> (<OS> <version>; <arch>) <terminal>` User-Agent.
The updated `codex-agentd` was built and tested on the remote Linux host, then
packaged over the verified P27 worker image without changing that earlier
image:

```text
worker image: localhost/codex-worker:agentd-glm-chat-ua-b88eb21e
archive:      /var/tmp/codex-worker-agentd-glm-chat-ua-b88eb21e.docker.tar
archive SHA:  91982ed4bce766e459804c53b145eff4628577224b2b9827053611139d9cc10a
agentd SHA:   b88eb21e038737b11885d8a50f64b0049eee434510d0d89e0690e72cd4f707a2
```

The archive was imported into the stopped LXC3900 template's K3s/containerd
image store. LXC3900 was restored to its original stopped template state,
including its original DHCP configuration and protected K3s agent environment.
The Controller's worker-image setting was backed up and changed to the new
immutable tag; the service restarted and its readiness probe returned HTTP 200.

A Controller-dispatched GLM 5.3 Flash task then ran on the new image:

```text
task:       task-7f827972c54bb6a8ec4b8e211d4c1b8c (SUCCEEDED)
attempt:    attempt-b0975cd8b1ef88c6055d18084a27b656 (COMPLETED)
VMID:       3001
validation: PASSED — codexua_probe.txt contains GLM_CONTROLLER_IMAGE_UA_OK
branch:     refs/heads/codex/task-7f827972c54bb6a8ec4b8e211d4c1b8c/attempt-b0975cd8b1ef88c6055d18084a27b656
commit:     e3283c44be8cc5daf4f2e0c19968b38913236bd2 (local; not pushed)
release:    DONE / COMPLETED; capacity claim reclaimed
```

The first automatic release reconciliation encountered a Kubernetes HTTP 409
while draining. After checking the exact task/attempt/VMID identity and that
the task Job/Pod were absent, the guarded Controller reconciliation endpoint
was used. Final read-only checks confirmed the task succeeded, the attempt
completed, the capacity claim was absent, LXC3001 and its PVE config were
absent, and its K3s Node was absent. No manual `pct destroy` was used. The
earlier VMID 3002 `UNKNOWN` claim and unrelated stale Nodes remain untouched.

This closes the updated-image availability and one-task Controller E2E gates
for the tested GLM Chat adapter path. P28 has since passed separately, as
recorded in `p28-secret-isolation-audit-2026-09-23.md`. This image test does
not close the OpenAI direct regression matrix, Muse Responses/tool-use, or the
broader P25 failure-injection matrix.
