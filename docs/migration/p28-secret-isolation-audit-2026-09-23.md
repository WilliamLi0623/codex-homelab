# P28 secret and isolation audit — 2026-09-23

P28 source tests and two live cross-task probes passed. The phase remains open
until the updated Controller is deployed and the two stopped worker containers
are safely reconciled and released. No worker was destroyed during this audit.

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

Release progress for both attempts is still `VERIFY_STOPPED/UNKNOWN`. The
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

The source changes have not yet been deployed to LXC210. The two live probes
therefore validate the existing runtime's cross-task workspace isolation, not
the newly added runtime identity and cleanup checks.

Verification:

```text
go test ./...                                      PASS
go test -race ./internal/executor/k3s ...          NOT RUN: cgo is disabled in this Go environment
```

The P28 gate requires deployment of the audited Controller build, explicit
reconciliation of the two release records, and verification that each worker
Job/Pod, Node, LXC, and claim is absent after release.
