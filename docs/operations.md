# V3 operations

The controller is the authoritative task interface. Operators use `codexctl`,
the REST API, MCP, or the task console; they do not SSH into normal workers.

`doctor` checks controller dependencies, provider profile health, and capacity
permissions. `status` reports persistent guests and dynamic capacity. `reconcile`
queries Proxmox, Kubernetes, Git, and GitHub before changing any `UNKNOWN`
outcome. Each task event stream records observable state transitions, command
references, validation, publication, and cleanup.

VM101 and WebCodex remain diagnostics and recovery paths. They are not the
normal task queue, task database, autoscaler, or model router.
