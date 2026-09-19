# V3 recovery

Persist intent before every infrastructure mutation. A timeout or interrupted
process produces `UNKNOWN`, never an automatic replay. Reconciliation queries
the deterministic identity before deciding whether to continue:

- Proxmox: VMID, generation metadata, guest type, and exact name.
- Kubernetes: deterministic Job and Node names.
- Git: remote branch and immutable commit SHA.
- GitHub: PR by deterministic branch.

Before a dynamic worker is destroyed, cordon and drain it, delete and verify
the Kubernetes Node, then verify Proxmox metadata before destroying the LXC.
Controller restart, K3s restart, worker loss, provider failure, gateway outage,
and publication timeouts are release gates, not manual-retry exceptions.
