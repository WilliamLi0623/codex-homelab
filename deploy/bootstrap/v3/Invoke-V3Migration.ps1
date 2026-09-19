[CmdletBinding()]
param(
  [switch]$ShowPlan
)

$ErrorActionPreference = "Stop"
$plan = [ordered]@{
  PLAN_VERSION = "v3-final"
  P0 = "Freeze the superseded V2 bootstrap"
  P1 = "Inventory Proxmox, recovery inputs, Git, and bootstrap state"
  P2 = "Preserve committed Git history and record intentional exclusions"
  P3 = "Replace V2 architecture documents and bootstrap target"
  P4 = "Implement Controller API, MCP, SQLite state, and codexctl"
  P5 = "Implement codex-agentd with Codex App Server primitives"
  P6 = "Discover and validate CC Hub model profiles without exposing secrets"
  P7 = "Run provider compatibility gates"
  P8 = "Create unprivileged LXC220 and install pinned K3s"
  P9 = "Run three disposable unprivileged K3s-agent lifecycle cycles"
  P10 = "Build the secret-free dynamic worker template"
  P11 = "Implement narrow Proxmox capacity management for VMIDs 3000-3999"
  P12 = "Implement K3s executor"
  P13 = "Run dedicated-LXC Controller to local-commit E2E"
  P14 = "Validate publication and Git unknown-outcome reconciliation"
  P15 = "Validate clean OpenAI, Muse, and GLM-5.3 Flash failover attempts"
  P16 = "Validate GLM-5.3 Flash worker tasks"
  P17 = "Add Kueue after plain K3s E2E"
  P18 = "Run failure injection"
  P19 = "Re-verify independent Windows bootstrap"
  P20 = "Compute migration readiness from actual evidence"
  P21 = "Reconcile legacy resources only if readiness is true"
  P22 = "Build final LXC210 control plane"
  P23 = "Build final VM101 special execution path"
  P24 = "Restrict Windows runner"
  P25 = "Run isolated production-style E2E"
  P26 = "Verify reboot recovery"
  P27 = "Verify scale-out and scale-in"
  P28 = "Run secret and isolation audit"
  P29 = "Run release CI"
  P30 = "Publish V3 operations documentation"
}

if ($ShowPlan) {
  $plan.GetEnumerator() | ForEach-Object { "{0}={1}" -f $_.Key, $_.Value }
  exit 0
}

throw "This command is a V3 phase manifest. Run it with -ShowPlan; phase implementations are invoked only by their dedicated, verified entrypoints."
