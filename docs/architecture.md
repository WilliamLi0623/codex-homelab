# Architecture

V0.1 responsibility boundaries:

- Proxmox: infrastructure lifecycle only.
- WebCodex: execution fabric only.
- controller-v2: logical task lifecycle only.
- SQLite: authoritative logical state.
- Git: immutable handoff/result.
- GitHub: ingress and human-facing result presentation.

The controller must not directly SSH to runners for task execution. Execution is abstracted behind the WebCodex adapter.