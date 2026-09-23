# P28 secret and isolation audit — 2026-09-23

P28 source tests and two live cross-task probes passed. The updated Controller
is deployed. The two explicitly authorized stopped worker containers have now
been reconciled and released through the Controller. P28's cleanup gate is
closed; no other VMID or historical UNKNOWN claim was touched.

## Secret scan

The audit scanned 129 commits reachable from refs and reflogs, plus two
unreachable commits and five unreachable blobs. It found no confirmed live
credential. `deploy/tunnel-client/codex-homelab-mcp.yaml` contains the literal
`env:CONTROL_PLANE_API_KEY`, which is a placeholder reference rather than a
key. Credential-shaped test values remain in test fixtures. The scan did not
print any candidate values.

No standard secret scanner (`gitleaks`, `trufflehog`, `detect-secrets`, or
`git-secrets`) was installed. The source scan used redacted `git grep` and
`rg` results. This heuristic scan cannot prove that binary, encoded, split, or
unusual-format credentials are absent.

## Live cross-task isolation probes

Two independent tasks ran concurrently on dedicated workers. Each task wrote a
different marker file, then validated that its own attempt ID, `CODEX_HOME`,
workspace path, and marker contents matched, and that the sibling marker was
absent.

| Task | Attempt | Worker | Validation | Local commit |
| --- | --- | ---: | --- | --- |
| `task-51d7e77031db4de9884c14aabe0aff78` | `attempt-2ea28962b78f57b2a0f9aecaa844e520` | 3001 | PASSED | `8d3ccea0f646b16232a278ce5e5bee4da81709e6` |
| `task-2f3e13cc6c588066869fcf53053f49a3` | `attempt-44c92ab228dfd21e258710b6a0d546ef` | 3003 | PASSED | `2a828543fe955367ca67fdc35d5b8b6cc0a2edfa` |

Both task records reached `SUCCEEDED`, and both validation rows are `PASSED`.
The worker Nodes, Jobs, and Pods are absent. Proxmox reports LXC 3001 and 3003
as `stopped`; their exact task/generation metadata matches these attempts.

At the initial audit checkpoint, release progress for both attempts was
`VERIFY_STOPPED/UNKNOWN`. The
Controller recorded `worker VMID <id> is not stopped`; later read-only checks
confirmed both containers are stopped. This is consistent with an immediate
status check racing Proxmox state propagation. The containers and durable
claims remain intact; the audit did not bypass the explicit
release-reconciliation gate.

## Source changes and verification

The implementation binds execution handles to both task and attempt IDs,
validates live Job and Pod identity before using them, and deletes the exact
attempt Job/Pods during the persisted `DRAIN` release step. It waits for their
absence before proceeding. `VerifyStopped` now polls Proxmox with a bounded
timeout and fails closed if identity checks or status observation fail.

The live probes ran before the new runtime identity and cleanup checks were
deployed, so they prove cross-task workspace isolation but not those new checks.
Commit `ef9caa4` is now running on LXC210. The active binary SHA256 is
`05c746a8d3f6ca71f479970dfae57629e13e2bd6160ddda32ac30e62f2b91a27`. The prior
binary is backed up as
`/usr/local/bin/codex-controller-v3.pre-p28-identity-20260923` with SHA256
`c4ded51f577cb2a609db4b8b8ac72a11699b9208a33e72c921196ba5b48e2fae`. The
service is active and `/v1/ready` returns `ready`.

Verification:

```text
go test ./...                                      PASS
go vet ./...                                       PASS
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build ./cmd/controller  PASS
go test -race ./internal/executor/k3s ...          NOT RUN: cgo is disabled in this Go environment
```

At the initial audit checkpoint, P28 required explicit authorization to
reconcile and destroy these exact stopped task workers, then verification that
each worker Job/Pod, Node, LXC, and capacity claim was absent after release.
That authorization was subsequently provided; completion evidence follows.
No other VMID was in scope.

## Authorized release completion — 2026-09-23

After explicit user authorization, both records were reconciled through the
Controller's `/release/reconcile` endpoint using the durable task/attempt,
VMID, generation, and Kubernetes node identity. The existing observer then
resumed the persisted release workflow; no direct `pct destroy` or database
claim deletion was used.

| VMID | Task / attempt | Release | Capacity claim | Proxmox config | K3s Node / Job / Pod |
| ---: | --- | --- | --- | --- | --- |
| 3001 | `task-51d7e77031db4de9884c14aabe0aff78` / `attempt-2ea28962b78f57b2a0f9aecaa844e520` | `DONE/COMPLETED` | absent | absent | absent |
| 3003 | `task-2f3e13cc6c588066869fcf53053f49a3` / `attempt-44c92ab228dfd21e258710b6a0d546ef` | `DONE/COMPLETED` | absent | absent | absent |

Final live checks confirmed the Controller `/v1/ready` endpoint remains ready.
The durable release rows retain the exact generations and node names above;
their errors are empty. The historical VMID 3002 UNKNOWN claim and all other
guests/claims remain untouched.
