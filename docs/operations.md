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

## Sending a task

The operator sends a task to the Controller on LXC210. The Controller is the
only component that talks to the Proxmox API; an operator does not send a task
directly to `pveproxy`, a dynamic LXC, or a K3s Pod.

The current fixed Controller address is:

```text
http://10.58.2.146:18080
```

For a health check from Windows:

```powershell
go run ./cmd/codexctl doctor --endpoint http://10.58.2.146:18080
go run ./cmd/codexctl status --endpoint http://10.58.2.146:18080
```

`codexctl run` is a convenient way to create an idempotent Task record:

```powershell
go run ./cmd/codexctl run `
  --endpoint http://10.58.2.146:18080 `
  --repository owner/repo `
  --base-ref main `
  --objective "Inspect the repository and fix the failing test" `
  --profile openai-primary `
  --idempotency-key task-20260922-001
```

The current CLI command creates the durable Task, but the authoritative live
dispatch sequence is still explicit: create an Attempt with the exact model
profile, then dispatch that Attempt with a prompt and validation command. The
`--profile` value on this convenience command is not a substitute for the
explicit Attempt profile selection below.

The following PowerShell example shows the complete Controller boundary without
placing a Proxmox token in the request:

```powershell
$Controller = "http://10.58.2.146:18080"
$Task = Invoke-RestMethod -Method Post -Uri "$Controller/v1/tasks" `
  -ContentType application/json -Body (@{
    repository = "owner/repo"
    base_ref = "main"
    objective = "Inspect the repository and fix the failing test"
    idempotency_key = "task-20260922-002"
  } | ConvertTo-Json)

$TaskID = $Task.task.id
$Attempt = Invoke-RestMethod -Method Post -Uri "$Controller/v1/tasks/$TaskID/attempts" `
  -ContentType application/json -Body (@{ profile = "openai-primary" } | ConvertTo-Json)

$AttemptID = $Attempt.attempt.id
Invoke-RestMethod -Method Post -Uri "$Controller/v1/tasks/$TaskID/dispatch" `
  -ContentType application/json -Body (@{
    attempt_id = $AttemptID
    prompt = "Inspect the repository, make the smallest safe fix, and run the tests."
    validation_command = @("sh", "-c", "go test ./...")
  } | ConvertTo-Json)
```

The dispatch path is:

```text
Controller
  -> Proxmox clone/start in VMID 3000-3999
  -> K3s node and isolated Job
  -> codex-agentd/Codex
  -> validation and local commit
  -> Controller publication and cleanup
```

The Controller owns Proxmox lifecycle, workspace isolation, credentials,
validation, commit, and cleanup. Do not SSH into a worker to run Codex or to
manually create a commit.

### Dynamic VMID allocation safety

The SQLite `capacity_nodes` ledger is not the only source of truth for a
dynamic VMID: stopped or manually-created Proxmox guests can exist outside the
ledger. Before cloning, the Controller performs a read-only Proxmox status
check and skips any VMID that is already present. If a VMID is acquired by a
different external operation in the small preflight-to-clone window, an exact
`CT <vmid> already exists` response is treated as a deterministic collision;
the uncreated ledger claim is removed and the next candidate is selected.

Network failures, timeouts, malformed responses, and other ambiguous provider
errors remain `UNKNOWN` and are never automatically replayed. This distinction
prevents a collision fix from weakening the no-duplicate-side-effect rule.

## Interacting with Codex after dispatch

Use the task and event APIs, rather than an interactive shell in the worker:

```powershell
go run ./cmd/codexctl list --endpoint http://10.58.2.146:18080
go run ./cmd/codexctl show TASK_ID --endpoint http://10.58.2.146:18080
go run ./cmd/codexctl logs TASK_ID --endpoint http://10.58.2.146:18080
go run ./cmd/codexctl send TASK_ID `
  --endpoint http://10.58.2.146:18080 `
  --body "Also inspect the failing test output and explain the remaining issue."
```

`send` currently appends a durable user follow-up message to the Task. It is
not SSH, a TTY, or a promise that a running worker will immediately receive a
new turn; the current API does not expose a Task-to-live-Job send endpoint.
The worker-side continuation seam exists internally, and a future interactive
phase must wire it to the durable message/event contract before claiming live
conversation support.

MCP exposes the same Controller boundary through `submit_task`,
`start_attempt`, `dispatch_task`, `get_task`, `get_task_events`, and
`send_message`. MCP and REST are equivalent control surfaces; neither bypasses
the Controller or sends credentials to Codex.

To cancel a task or retry a terminal task:

```powershell
go run ./cmd/codexctl cancel TASK_ID --endpoint http://10.58.2.146:18080
go run ./cmd/codexctl retry TASK_ID --endpoint http://10.58.2.146:18080
```

Do not mark an UNKNOWN attempt as failed or completed from intuition. Use
`codexctl reconcile` only after checking the Controller, Kubernetes, Proxmox,
Git, and publication evidence.
