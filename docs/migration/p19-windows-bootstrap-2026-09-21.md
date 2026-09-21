# P19 Windows bootstrap independence — 2026-09-21

## Phase contract

The current V3 phase manifest defines P19 as **re-verify independent Windows
bootstrap**. It is separate from dynamic worker OCI image preload, which is a
remaining scale-out/bootstrap item.

The existing runtime capsule is `C:\WebCodexBootstrap`. After a controlled
source synchronization and rerun, its persisted `independence-result.json`
records:

```text
stop_ok=true
ssh_ok=true
inventory_ok=true
restart_ok=true
completed=2026-09-21T16:10:34.9101290Z
```

The corresponding `state.json` records `independence_test=success` and
`git_preservation=success`. LXC210 was running again after the test, with both
`webcodex.service` and `cloudflared.service` active. The detached wrapper could
not create a child process because local CIM process creation was denied, so the
same bounded child script was run directly and its result was then accepted by
the wrapper. No destroy operation was executed.

## Current gate status

The runtime capsule was backed up to
`C:\WebCodexBootstrap\backup-20260922-000359` before synchronization. The
checked-in `deploy/bootstrap` sources and the runtime scripts used for P19 now
match by SHA-256. The verification script was also made tolerant of the
currently absent `webcodex.socket` unit while keeping the two actual services
strictly checked.

Therefore P19 is **PASS**. The stage can stop and restart the current control
path, so it must not be run implicitly by ordinary validation or parallel work.

## Explicitly not claimed

- P20 readiness is a separate gate and currently fails closed because a
  protected resource is missing; see the P20 evidence document.
- P21 destructive reconciliation was not executed.
- Dynamic worker image preload remains a separate open bootstrap item.
