# P21 legacy reconciliation — 2026-09-22

## Result

P21 **PASS**. The user explicitly authorized the destructive reconciliation
after P20 reported `READY_FOR_DESTRUCTION=true`.

The checked-in stage 20 script was run from the synchronized
`C:\WebCodexBootstrap` capsule. It reconciled the remaining legacy targets:

```text
VM 101  codex-runner-01  destroyed
LXC 210 codex-control     destroyed
```

Already absent legacy targets were recorded as idempotent skips. No protected
resource was targeted.

## Verification

- `qm config 101` reports no configuration file.
- `pct config 210` reports no configuration file.
- `local`, `pool`, and `backup` have no storage entries for 101 or 210.
- Protected LXC100 remains `tailscale-alt`.
- Protected LXC200 remains `grafana-monitor`.
- Runtime `state.json` records `destroy=success`.
- The stage exited with code 0.

LXC110 was not touched by P21; it was already absent and had been explicitly
retired before this run.

## Next phases

The legacy reconciliation gate is complete. The next work is P22/P23: build
the final LXC210 control plane and final VM101 special execution path, followed
by the later production, recovery, scale, security, and release gates.
