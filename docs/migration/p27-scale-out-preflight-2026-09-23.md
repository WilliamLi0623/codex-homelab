# P27 scale-out preflight — 2026-09-23

This note records the read-only resource check before a concurrent dedicated-LXC scale-out test.

## Result

The scale-out test did not start because the Proxmox host had insufficient memory headroom for multiple workers at the configured template limit.

```text
PVE host memory:       62 GiB total, 57 GiB used, 5 GiB available
PVE host swap:         975 MiB used, 96 MiB free
LXC3900 worker limit:  4096 MiB, 2 cores
```

Two simultaneous workers could each use up to 4 GiB. Starting them with only 5 GiB available and almost no free swap would risk host memory pressure, so no concurrent worker was allocated.

## Preserved live state

The preflight made no infrastructure changes. It did not delete or reconcile the four historical `UNKNOWN` capacity claims for VMIDs `3000`, `3011`, `3012`, and `3014`. It also left the old P13 Jobs and Pods untouched:

```text
codex-p13-e2e-20260920-r7  Pod: CreateContainerConfigError
codex-p13-e2e-20260920-r8  Pod: CreateContainerConfigError
```

No disposable worker was running after P26 cleanup. LXC3006, the fixed infrastructure guests, templates, and VM101 were not changed.

## Gate status

P27 scale-out/scale-in is still open. The completed P26 probes prove single-worker clone, execution, validation, local commit, restart recovery, and release; they do not prove concurrent scale-out. Resume the concurrency test after the host has adequate memory headroom or after an explicitly approved worker-memory adjustment. Do not stop fixed guests or alter template memory as an implicit workaround.

The P28 secret/isolation audit remains a later gate; this preflight did not run a complete Git-history secret scan or live cross-task isolation test.
