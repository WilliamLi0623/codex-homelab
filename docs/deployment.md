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
App Server with an explicit attempt-specific `CODEX_HOME`, reads newline-
delimited JSON requests containing `prompt` from stdin, and emits one result
per request. The first request starts a thread; later requests resume that
same thread before starting a new turn. Results contain a thread ID and event
method names. When the external validation/publication flow writes a result
file, it may set `CODEX_COMMIT_SHA_FILE` to an absolute, attempt-scoped path.
After a successful turn, agentd reads that file and includes optional
`commit_sha` only when the file content is exactly 40 hexadecimal characters.
Missing, unreadable, or invalid files leave the field absent; agentd does not
infer the SHA or run Git. The file is read again after each successful
follow-up turn. The worker requires non-empty `CODEX_HOME` and
`CODEX_ATTEMPT_ID`; the final directory name of `CODEX_HOME` must equal the
safe attempt ID. For a persistent Pod worker, pass `--listen 0.0.0.0:8080`;
the worker then exposes `GET /v1/healthz` and `POST /v1/messages` over the
Pod proxy while preserving one Codex thread. Build a Linux amd64 artifact with:

```powershell
$env:GOOS = "linux"
$env:GOARCH = "amd64"
$env:CGO_ENABLED = "0"
go build -trimpath -ldflags "-s -w" -o codex-agentd ./cmd/agentd
```

The Proxmox template gate remains open until the template contains both this
entrypoint and the pinned Codex CLI/runtime. No model key or `CODEX_HOME`
contents belong in the template.

## Live storage and P22–P25 checkpoint

The current control and special-runner system disks are on `local` SSD storage:
LXC210 uses a 32G `local` rootfs and VM101 uses a 100G `local` system disk.
The `pool` storage class is reserved for data/workloads that explicitly need
the mechanical array; dynamic worker system rootfs remains on `local`.

The live P22–P25 evidence, including the successful GLM Chat Completions
terminal loop and release reconciliation, is recorded in
`docs/migration/p22-p25-runtime-2026-09-22.md`.
