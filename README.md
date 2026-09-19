# codex-homelab

Minimal, durable Codex/WebCodex homelab control and execution plane.

V0.1 intentionally contains only:
- one control-plane LXC running WebCodex Server + controller-v2 + SQLite
- one execution-plane VM running WebCodex Runner + Codex CLI
- GitHub as ingress/result UX
- immutable Git commits as task outputs

It intentionally excludes k3s, Kueue, dynamic Proxmox workers, per-task VMs, legacy GitHub-as-queue scheduling, and multi-agent DAGs.

Bootstrap source lives under deploy/bootstrap. Copy it to C:\WebCodexBootstrap, fill the private runtime config, and run through stage 19 first. Destructive stages require both a computed readiness gate and the explicit -AllowDestruction switch.

See docs/architecture.md, docs/deployment.md, docs/recovery.md, and SECURITY.md.