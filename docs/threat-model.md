# V3 threat model

V3 defends against credential leakage, task cross-contamination, stale or
ambiguous infrastructure outcomes, unsafe worker privileges, and accidental
modification of protected infrastructure.

Controls include privileged-but-bounded Codex LXC profiles, no arbitrary host
mounts or Docker sockets, metadata-bound VMID range checks, a denylist for
persistent guests, per-attempt worktrees and `CODEX_HOME`, deterministic
identities, and reconciliation before mutation replay. The only explicit
host device is `/dev/kmsg` for K3s, and nested containerd AppArmor loading is
disabled by a scoped drop-in. CC Hub gateway failures are distinct from
logical model failures so the router does not retry both gateway routes during
a complete gateway outage.
