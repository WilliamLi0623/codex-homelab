# Muse terminal tool-use adapter Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a fail-closed Muse Spark Responses tool loop that executes bounded terminal calls in the attempt workspace and lets a real P13 task reach validation and local commit.

**Architecture:** Keep the existing Codex App Server runner for `openai-primary`. Add a provider-neutral Responses transport and a Muse-specific runner behind the existing `runner` interface. The runner owns only provider conversation and terminal tool execution; workspace preparation, validation, commit, result persistence, and release remain in the existing layers.

**Tech Stack:** Go, `net/http`, JSON, existing `internal/agentd` runner interfaces, `os/exec`, Go tests, Kubernetes worker image.

**Spec:** `docs/superpowers/specs/2026-09-21-muse-terminal-tool-loop-design.md`

## Global Constraints

- GLM-5.3 Flash remains fail-closed because its coding-agent adapter is not implemented.
- OpenAI/Codex App Server behavior remains unchanged.
- Terminal execution is restricted to the attempt-scoped `CODEX_WORKSPACE`.
- API keys remain Kubernetes Secret-backed and never enter logs, tool output, config files, or repositories.
- No remote push, SSH, Proxmox, Kubernetes, or secret-file access is exposed to Muse.
- Every production behavior change requires a failing test before implementation.

---

### Task 1: Define the Muse Responses protocol boundary

**Files:**
- Create: `internal/muse/responses.go`
- Create: `internal/muse/responses_test.go`

**Interfaces:**
- Produces `type Transport interface { Do(context.Context, Request) (Response, error) }`.
- Produces `type Request struct { Model string; Input []InputItem; PreviousResponseID string; Tools []Tool }`.
- Produces `type Response struct { ID string; Output []OutputItem; Status string }`.
- Produces typed tool-call and final-text decoding without exposing provider JSON to `cmd/agentd`.

- [ ] **Step 1: Write the failing protocol tests** for encoding a Muse Responses request with model, input, previous response ID, and terminal tool schema; decode one tool call and one final response; reject malformed JSON and missing response IDs.
- [ ] **Step 2: Run** `go test ./internal/muse -run 'TestResponses' -count=1` and verify the new tests fail because the protocol package does not exist.
- [ ] **Step 3: Implement** the request/response structs, JSON marshal/unmarshal helpers, and bounded diagnostic errors using `encoding/json`.
- [ ] **Step 4: Run** the same targeted command and verify PASS.
- [ ] **Step 5: Run** `go vet ./internal/muse`.

### Task 2: Implement bounded terminal execution

**Files:**
- Create: `internal/muse/terminal.go`
- Create: `internal/muse/terminal_test.go`

**Interfaces:**
- Produces `type Terminal struct { Workspace string; Timeout time.Duration; MaxOutputBytes int; MaxCommands int }`.
- Produces `func (t Terminal) Run(context.Context, string) (ToolResult, error)`.
- `ToolResult` contains exit code, bounded stdout, bounded stderr, and a truncation flag.

- [ ] **Step 1: Write failing tests** for a command that creates a file in the workspace, path escape through a working-directory argument, timeout, non-zero exit, output truncation, and command-count exhaustion.
- [ ] **Step 2: Run** `go test ./internal/muse -run 'TestTerminal' -count=1` and verify failure before implementation.
- [ ] **Step 3: Implement** `exec.CommandContext` with `cmd.Dir = Workspace`, reject commands containing a supplied directory outside the workspace, capture combined output with a hard byte cap, and return structured exit information.
- [ ] **Step 4: Run** the targeted tests and verify PASS.
- [ ] **Step 5: Add tests** proving environment inheritance does not expose provider request fields or credentials to the tool result.

### Task 3: Implement the Muse Responses tool loop

**Files:**
- Create: `internal/muse/runner.go`
- Create: `internal/muse/runner_test.go`

**Interfaces:**
- Produces `type Client interface { Create(context.Context, Request) (Response, error) }`.
- Produces `type Runner struct { Client Client; Terminal Terminal; Model string; MaxTurns int }`.
- Produces `func (r Runner) Run(context.Context, string) (string, []agentd.Event, error)` through an internal event callback or a package-local event type that `cmd/agentd` can convert.

- [ ] **Step 1: Write failing fake-client tests** for: first response containing a terminal call, continuation containing the tool result, final response termination, multiple calls in one response, provider error, malformed tool arguments, and max-turn exhaustion.
- [ ] **Step 2: Run** `go test ./internal/muse -run 'TestRunner' -count=1` and verify the expected failures.
- [ ] **Step 3: Implement** the loop: send the initial prompt and tool schema; execute only the named terminal tool; submit tool output with the response ID; stop on final assistant output; wrap errors with provider/model/turn/tool context.
- [ ] **Step 4: Run** targeted tests and verify PASS.
- [ ] **Step 5: Add assertions** that no commit, push, or validation command is executed by the Muse runner itself.

### Task 4: Add the HTTPS CCH Responses transport

**Files:**
- Create: `internal/muse/http_client.go`
- Create: `internal/muse/http_client_test.go`

**Interfaces:**
- Produces `type HTTPClient struct { BaseURL string; APIKey string; HTTPClient *http.Client }`.
- Produces `func (c HTTPClient) Create(context.Context, Request) (Response, error)`.

- [ ] **Step 1: Write failing `httptest.Server` tests** for authorization header, `/v1/responses` path, JSON request body, successful decode, non-2xx response body redaction, timeout, and no API-key leakage in returned errors.
- [ ] **Step 2: Run** `go test ./internal/muse -run 'TestHTTPClient' -count=1` and verify failure.
- [ ] **Step 3: Implement** URL joining, request timeout propagation, `Authorization: Bearer`, JSON decoding, bounded error-body capture, and redaction.
- [ ] **Step 4: Run** targeted tests and verify PASS.

### Task 5: Route agentd by explicit provider profile

**Files:**
- Modify: `cmd/agentd/main.go`
- Modify: `cmd/agentd/config.go`
- Modify: `cmd/agentd/config_test.go`
- Modify: `cmd/agentd/session_test.go`

**Interfaces:**
- Add a runner constructor that selects `muse.Runner` only for exact profile/model `muse-spark-1.3-contributor` and only when `CODEX_WIRE_API=responses`.
- Preserve `StartCodexAppServer` for `openai-primary`.
- Reject `glm-5.3-flash` and `CODEX_WIRE_API=coding-agent` with the existing fail-closed error.

- [ ] **Step 1: Write failing tests** proving Muse selects the HTTP/tool-loop runner, OpenAI selects App Server, GLM remains rejected, and missing workspace/provider settings fail closed.
- [ ] **Step 2: Run** `go test ./cmd/agentd -run 'Test.*Runner|Test.*Config' -count=1` and verify failure.
- [ ] **Step 3: Implement** a small runner factory that receives environment values, constructs the CCH HTTP client from `CODEX_OPENAI_BASE_URL` and `CODEX_API_KEY`, and injects attempt workspace limits into `muse.Terminal`.
- [ ] **Step 4: Adapt** `session` to consume the common runner result/events without changing validation or commit code.
- [ ] **Step 5: Run** targeted tests and verify PASS.

### Task 6: Integrate worker image and provider metadata

**Files:**
- Modify: `internal/executor/k3s/kubernetes_runtime.go`
- Modify: `internal/executor/k3s/kubernetes_runtime_test.go`
- Modify: `cmd/controller/config.go`
- Modify: `cmd/controller/config_test.go`
- Modify: `docs/migration/p15-provider-routing-2026-09-20.md`

**Interfaces:**
- Ensure Muse Jobs receive `CODEX_MODEL=muse-spark-1.3-contributor`, `CODEX_WIRE_API=responses`, `CODEX_OPENAI_BASE_URL`, `CODEX_MODEL_PROFILE`, and Secret-backed `CODEX_API_KEY`.
- Ensure OpenAI and GLM selection remains explicit and fail-closed where unsupported.

- [ ] **Step 1: Write failing manifest tests** for exact Muse env and for rejecting a profile/model mismatch.
- [ ] **Step 2: Run** `go test ./internal/executor/k3s ./cmd/controller -run 'Test.*Model|Test.*Profile' -count=1` and verify failure.
- [ ] **Step 3: Implement** the environment/profile mapping without putting credentials into the manifest value fields.
- [ ] **Step 4: Run** targeted tests and verify PASS.
- [ ] **Step 5: Update** provider-routing documentation with the adapter state and exact fail-closed behavior.

### Task 7: Build, deploy, and execute the real P13 acceptance gate

**Files:**
- Modify: `docs/migration/p13-readiness-2026-09-20.md`
- Create: `artifacts/codex-agentd-muse-linux-amd64` as a local build artifact only.

- [ ] **Step 1: Run** `go test ./...`, `go vet ./...`, and `git diff --check`; record the existing Windows SQLite cleanup race separately if it recurs.
- [ ] **Step 2: Build** the Linux agentd binary with `GOOS=linux GOARCH=amd64 CGO_ENABLED=0` and record its SHA256.
- [ ] **Step 3: Repack** the worker OCI archive with the new binary, import it into the Controller-assigned node, and verify the embedded binary hash.
- [ ] **Step 4: Dispatch** a task with profile `muse-spark-1.3-contributor` and a marker-file validation command.
- [ ] **Step 5: Verify** Pod logs show terminal tool calls, the validation command passes, the result includes a 40-character commit SHA, task events include completion, and the capacity/release record reaches its terminal state.
- [ ] **Step 6: If any gate fails**, preserve the exact provider/turn/tool error and do not mark P13 complete.