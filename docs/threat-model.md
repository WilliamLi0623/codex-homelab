# V3 threat model

V3 defends against credential leakage, task cross-contamination, stale or
ambiguous infrastructure outcomes, unsafe worker privileges, and accidental
modification of protected infrastructure.

Controls include unprivileged LXC compatibility gates, no host mounts or
Docker sockets, metadata-bound VMID range checks, a denylist for persistent
guests, per-attempt worktrees and `CODEX_HOME`, deterministic identities, and
reconciliation before mutation replay. CC Hub gateway failures are distinct
from logical model failures so the router does not retry both gateway routes
during a complete gateway outage.
