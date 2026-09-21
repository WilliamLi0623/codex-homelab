# P19 Windows bootstrap independence — 2026-09-21

## Phase contract

The current V3 phase manifest defines P19 as **re-verify independent Windows
bootstrap**. It is separate from dynamic worker OCI image preload, which is a
remaining scale-out/bootstrap item.

The existing runtime capsule is `C:\WebCodexBootstrap`. Its persisted
`independence-result.json` records:

```text
stop_ok=true
ssh_ok=true
inventory_ok=true
restart_ok=true
completed=2026-09-19T06:43:55.7068234Z
```

The corresponding `state.json` records `independence_test=success` and
`git_preservation=success`. This is historical evidence that the detached
PowerShell → Windows SSH → Proxmox path completed and the old control path was
restored.

## Current gate status

The historical result cannot yet be promoted to current-source P19 evidence.
The checked-in `deploy/bootstrap` sources and the staged runtime capsule drift:

- `rebuild.ps1` and `Freeze-V2.ps1` match;
- `Common.ps1` and the stage scripts used by the independence/readiness flow do
  not match by SHA-256.

Therefore the current status is **PARTIAL / REQUIRES CONTROLLED RERUN**. The
rerun must first synchronize the capsule from the checked-in source, then run
the bounded independence stage and verify its persisted result. The stage can
stop and restart the current control path, so it must not be run implicitly by
ordinary validation or parallel work.

## Explicitly not claimed

- P20 readiness is not declared.
- `READY_FOR_DESTRUCTION=true` is not regenerated from the drifted capsule.
- No current LXC210 service was stopped or restarted during this audit.
- Dynamic worker image preload remains a separate open bootstrap item.
