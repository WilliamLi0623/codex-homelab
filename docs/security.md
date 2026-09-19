# V3 security model

The controller runtime uses a narrow Proxmox identity limited to dynamic VMIDs
3000–3999. It cannot modify protected LXCs, persistent guests, host network,
or host storage. Static migration uses separately protected administration
credentials.

`/root/litellm_key` is a root-owned `0600` deployment source only. Bootstrap
creates a scoped Kubernetes Secret when a CC Hub ModelProfile needs it. Keys,
OpenAI authentication, GitHub tokens, Proxmox credentials, and private SSH
keys are absent from Git, worker templates, images, logs, and task worktrees.

Every task has a clean worktree and attempt-specific `CODEX_HOME`. Windows is
limited to `C:\WebCodexWorkspace`; it is administration and recovery, not
heavy execution.
