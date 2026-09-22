# V3 security model

The controller runtime uses a narrow Proxmox identity limited to dynamic VMIDs
3000–3999. It cannot modify protected LXCs, persistent guests, host network,
or host storage. Static migration uses separately protected administration
credentials.

The CC Hub key host, key path, and dashboard URL belong only in private runtime
configuration. The source file is root-owned `0600`; the dashboard is not
assumed to be the API Base URL. Bootstrap creates a scoped Kubernetes Secret
when a CC Hub ModelProfile needs it. Keys, OpenAI authentication, GitHub tokens,
Proxmox credentials, and private SSH keys are absent from Git, worker templates,
images, logs, and task worktrees.

Every task has a clean worktree and attempt-specific `CODEX_HOME`. Windows is
limited to `C:\WebCodexWorkspace`; it is administration and recovery, not
heavy execution.

## ChatGPT MCP app boundary

The optional ChatGPT interface uses the private MCP gateway and Secure MCP
Tunnel. The gateway has its own bearer token and must not receive Proxmox,
Kubernetes, CCH, GitHub, or OpenAI credentials as tool arguments.

The gateway is read-only for dispatch/continuation unless an authenticated
`MCP_GATEWAY_CONTROLLER_URL` adapter is configured. That adapter forwards to the
existing Controller HTTP API; it may not open a second K3s/Proxmox execution
path or silently fall back to append-only `send_message`.
