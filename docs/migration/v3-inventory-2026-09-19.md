# V3 live inventory — 2026-09-19

This report records the live pre-migration state for Codex Homelab V3. It is
an observation, not an authorization to mutate any resource.

## Guests

| Type | ID | Name | State | V3 disposition |
| --- | ---: | --- | --- | --- |
| VM | 101 | `codex-runner-01` | running | Reconcile as the future special-runner candidate; normal work must not use it. |
| LXC | 100 | `tailscale-alt` | running | Protected; do not modify. |
| LXC | 110 | `gpt-oss-cpu-bench` | running | Protected; do not modify. |
| LXC | 200 | `grafana-monitor` | running | Protected; do not modify. |
| LXC | 210 | `codex-control` | running | Existing V2 control guest; rebuild only after V3 readiness gates. |

The V2 allowlist (101, 102, 104–109, 9701, 103, and the former LXC210)
was already destroyed before V3 began. V3 therefore proceeds from the actual
state above; it does not assert that a pre-destruction gate can be replayed.

## Proxmox capacity and bootstrap inputs

- `vmbr0` is up at `10.58.2.187/24`.
- `local` and `pool` storage are active with substantial available capacity.
- Ubuntu 24.04 LXC and cloud-image artifacts are available.
- Recovery assets exist on the Proxmox host. Their contents are not inspected
  or recorded here.
- The CC Hub deployment source was verified non-empty and corrected to
  `root:root 0600`; its host and path stay in private runtime configuration and
  its contents were not inspected. P6 must still discover the API Base URL and
  exact model IDs before enabling any CC Hub profile.

## Windows bootstrap

`C:\WebCodexBootstrap` remains present and independent of legacy WebCodex.
The V3 freeze marker is present. Its `rebuild.ps1` hash matches the V3 source
freeze guard at the time of this inventory.
