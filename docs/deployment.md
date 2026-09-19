# V3 deployment gates

V3 is staged so that dynamic capacity is proven before it becomes the default. The Windows bootstrap remains an independent recovery path, but its V2 entrypoint is frozen.

1. Freeze V2, inventory live state, preserve Git, and publish the V3 source.
2. Implement and test Controller, codex-agentd, model discovery, and provider compatibility without enabling an unverified model.
3. Create unprivileged LXC220 and prove K3s-agent compatibility in at least three disposable lifecycle cycles. Stop migration if privileges, host mounts, Docker sockets, host keys, or unsafe devices are required.
4. Build the secret-free template, narrow runtime Proxmox identity, and K3s executor. Demonstrate dedicated-LXC commit and publication flows.
5. Add Kueue only after plain K3s succeeds. Complete failover, recovery, security, and release CI gates before destructive reconciliation.

The V3 source manifest is inspectable with:

```powershell
pwsh -NoProfile -File .\deploy\bootstrap\v3\Invoke-V3Migration.ps1 -ShowPlan
```

Do not use this command to provision infrastructure; it only reports the checked-in phase contract.

## Worker runtime gate

`cmd/agentd` is the headless worker entrypoint. It starts the official Codex
App Server with an explicit attempt-specific `CODEX_HOME`, reads one JSON
request containing `prompt` from stdin, and emits only a thread ID and event
method names. Build a Linux amd64 artifact with:

```powershell
$env:GOOS = "linux"
$env:GOARCH = "amd64"
$env:CGO_ENABLED = "0"
go build -trimpath -ldflags "-s -w" -o codex-agentd ./cmd/agentd
```

The Proxmox template gate remains open until the template contains both this
entrypoint and the pinned Codex CLI/runtime. No model key or `CODEX_HOME`
contents belong in the template.