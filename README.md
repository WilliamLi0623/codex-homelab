# Codex Homelab

An experimental control plane for isolated, headless coding tasks on dynamic Proxmox LXCs.

Codex Homelab V3 accepts a task through an API, MCP, CLI, or task console; the controller selects a model and execution class; a dedicated LXC joins K3s; Codex produces a local commit; and the controller publishes a branch and PR.

## Status

V3 migration is in progress. The former V2 bootstrap is frozen in the runtime environment and cannot run. Do not treat this repository as a production orchestration system until compatibility, failure-injection, and security gates pass.

## Inspect the migration plan

From a clone of this repository, print the checked-in V3 phase manifest:

```powershell
pwsh -NoProfile -File .\deploy\bootstrap\v3\Invoke-V3Migration.ps1 -ShowPlan
```

The command is read-only. It prints `PLAN_VERSION=v3-final` followed by P0 through P30.

## Execution model

- **Normal tasks:** `dedicated-lxc` is the default. One active task receives one disposable LXC in VMID range 3000–3999.
- **Special tasks:** `vm-special` uses VM101 only for incompatible, dangerous, or recovery work. Normal tasks must not route there.
- **Models:** OpenAI/Codex is primary; Muse Spark 1.3 Contributor is the first provider-failure fallback; MiMo 2.5 is the second fallback and low-cost worker model.
- **Secrets:** Runtime model credentials are never committed, embedded in an LXC template, or mounted from Proxmox into a task.

Read [architecture](docs/architecture.md) for responsibility boundaries, [deployment](docs/deployment.md) for staged gates, and [security](docs/security.md) for non-negotiable isolation rules.

## License

See [LICENSE](LICENSE).
