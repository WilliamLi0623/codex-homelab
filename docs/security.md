# Security model

The control plane owns logical state but not hypervisor scheduling credentials for task execution. The runner executes as a non-root user with a limited writable root and isolated worktrees.