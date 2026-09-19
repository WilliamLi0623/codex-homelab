# V3 architecture

The controller decides **what** logical task happens. The model router decides **which** model handles an attempt. The execution broker decides **which execution class** applies. Capacity Manager creates and removes dynamic LXCs; K3s places Pods; Kueue later controls admission and quota. These responsibilities must remain separate.

## Persistent roles

| Resource | Role | Normal coding work |
| --- | --- | --- |
| LXC210 `codex-control` | controller API, MCP, SQLite, task console, router, broker, capacity manager | forbidden |
| LXC220 `k3s-control` | unprivileged K3s server and control-plane services | forbidden by taint |
| VM101 `codex-special-runner` | exceptional compatibility, recovery, and dangerous workloads | explicit `vm-special` only |
| LXC3000–3999 | dedicated or shared K3s agent capacity | default path |

## Task path

`REST/MCP/codexctl/UI → Controller → Model Router → Execution Broker → Capacity Manager → Proxmox LXC → K3s agent → task Job → codex-agentd → Codex → validation → immutable commit → controller publication`.

Every attempt uses an isolated worktree and attempt-specific `CODEX_HOME`. Provider failure starts a fresh attempt from the recorded base commit; a dirty workspace is never handed from OpenAI to Muse or GLM-5.3 Flash. The current routing order is OpenAI primary, Muse first failover, and GLM-5.3 Flash in the former MiMo second-failover/low-cost-worker position.

## Invariants

- `dedicated-lxc` is the global default; shared LXC and VM101 require policy.
- Only Capacity Manager may mutate VMIDs 3000–3999. Its runtime identity may not modify persistent or protected guests.
- Dynamic workers contain no provider key, Proxmox credential, GitHub token, private SSH key, or task data before claim.
- `UNKNOWN` is a reconciliation state, not permission to replay a mutation.
- Observable events may contain commands and model-provided summaries, never hidden chain-of-thought.
