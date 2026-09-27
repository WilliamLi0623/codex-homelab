# P27 Task 7 report

## Changed files

- `internal/mcp/controller_client.go`: added Controller API adapters for start-attempt and retry, decoding the existing Controller response into MCP-compatible domain values. These methods do not resolve routes or write attempts locally.
- `internal/mcp/server.go`: added an optional attempt-creation interface. When the configured dispatcher implements it (the production `ControllerClient` does), MCP start/retry delegate to Controller; when it does not, local-only mode retains the existing store calls.
- `internal/mcp/server_test.go`: added an integration regression test using the existing API server, shared SQLite store, and Controller dispatch path. It verifies normal and quota-fallback snapshots for both start and retry, then verifies dispatch consumes the matching frozen route.

No `cmd/mcp-gateway` wiring was required because its existing Controller client constructor already supplies the dispatcher. No routing policy, store, API, infrastructure, deployment, config, or credential handling was changed.

## TDD RED

Command:

```text
go test ./internal/mcp -run TestControllerForwardingCreatesFrozenRoutesForStartAndRetry -count=1
```

The first attempt was blocked before compilation by the command environment's access-denied Go build cache and telemetry-token paths. Re-running the same test with command-scoped temporary `GOCACHE`/`GOTMPDIR` and `GOTELEMETRY=off` produced the intended failure:

```text
--- FAIL: TestControllerForwardingCreatesFrozenRoutesForStartAndRetry
    server_test.go:93: start route = attempt route snapshot not found
FAIL
```

This was the expected RED result: current MCP start-attempt used the legacy local store method, so the Controller's route-aware endpoint was never called and no snapshot existed.

## GREEN and broader verification

Focused regression and existing MCP tests passed after implementation:

```text
go test ./internal/mcp -run 'TestControllerForwardingCreatesFrozenRoutesForStartAndRetry|Test(StartAttemptCreatesInitialMCPAttempt|RetryCreatesANewAppendOnlyAttempt|DispatchTask)$' -count=1
ok github.com/WilliamLi0623/codex-homelab/internal/mcp
```

Required command passed:

```text
go test ./internal/mcp ./cmd/mcp-gateway ./internal/api -count=1
ok github.com/WilliamLi0623/codex-homelab/internal/mcp
ok github.com/WilliamLi0623/codex-homelab/cmd/mcp-gateway
ok github.com/WilliamLi0623/codex-homelab/internal/api
```

Both Go runs emitted the pre-existing command-host warning that the Go telemetry upload token could not be created under the user profile due to access denied. It did not affect test results; command-scoped caches were used and no project configuration was changed.

## Self-review

- Controller-forwarding start/retry now uses only the existing `/v1/tasks/{id}/attempts` and `/v1/tasks/{id}/retry` APIs.
- Route resolution and atomic frozen-snapshot persistence remain exclusively in Controller/API code.
- Local-only construction still calls the original store methods and retains fail-closed dispatch when no dispatcher is configured.
- MCP arguments contain no route values or credentials; the client only sends the existing profile/task inputs and Controller bearer header.
- Existing append-only, idempotency, UNKNOWN reconciliation, and lifecycle behavior is not duplicated or altered.
- `git diff --check` passed. Only the allowed MCP source/test files plus this requested report are part of the task change. Pre-existing untracked caches/artifacts were left untouched.

## Risks and remaining boundaries

- The MCP test uses the existing in-process Controller HTTP server and shared SQLite store; it does not deploy or restart services, and it does not prove production network/authentication beyond the existing HTTP client tests.
- Go telemetry access remains an environment warning and is unrelated to this change.
