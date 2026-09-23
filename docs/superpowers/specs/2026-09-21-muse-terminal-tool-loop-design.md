# Muse terminal tool-use adapter design

Date: 2026-09-21
Status: Approved in conversation; awaiting written-spec review before implementation

## Problem

The current worker starts the Codex App Server with Muse Spark through the
Responses endpoint. Real Controller Jobs authenticate, clone, start the App
Server, and reach validation, but Muse emits no terminal tool call. The worker
therefore cannot create files or complete the validation/commit contract.

The existing OpenAI/Codex App Server path must remain unchanged. GLM-5.3 Flash
remains fail-closed because its separate coding-agent adapter is not yet
implemented.

## Design

Add a provider-specific Muse Responses tool-loop runner behind the existing
agentd session runner boundary.

- `openai-primary` keeps the current App Server runner.
- `muse-spark-1.3-contributor` selects the Muse runner explicitly through the
  persisted attempt profile and worker environment.
- The Muse runner sends Responses requests to the configured CCH `/v1/responses`
  endpoint with an explicit terminal tool schema.
- Each tool call is validated and executed only inside the attempt workspace.
  The runner returns bounded stdout/stderr and exit status as the tool result,
  then continues the Responses loop until a final assistant result or a hard
  limit is reached.
- The existing workspace preparation, validation, local commit, result
  protocol, and Controller persistence remain authoritative and are not
  duplicated in the provider adapter.

## Tool and safety contract

The adapter exposes one terminal tool with a command string and optional
working directory constrained to `CODEX_WORKSPACE`. It enforces:

- no execution outside the attempt workspace;
- no provider-supplied environment mutation or credential access;
- bounded command count, command timeout, output bytes, and response turns;
- no remote push, SSH, Proxmox, Kubernetes, or secret-file access;
- explicit errors for malformed tool calls, unsupported tool names, timeout,
  non-zero exit, output truncation, and loop exhaustion.

The adapter runs with the existing attempt-scoped `CODEX_HOME` and receives the
API key only from the Kubernetes Secret-backed environment. It never writes the
key to request logs, tool output, or the repository.

## Error and event behavior

Provider transport errors, malformed Responses payloads, tool execution errors,
and loop exhaustion are wrapped with the provider, turn number, tool name, and
bounded diagnostic output. The current agentd error chain remains visible to
Kubernetes logs. A successful final response proceeds to the existing
validation and commit stage; no commit occurs when the loop or validation
fails.

## Tests and acceptance

Unit tests must cover:

- Responses request model, tools, and continuation payload;
- a tool call that creates a file in the workspace;
- rejection of path escape, unsupported tools, timeout, and oversized output;
- multiple tool calls and final-response termination;
- provider error, malformed payload, and loop-limit error chains;
- profile selection while preserving the existing App Server path.

Acceptance requires a real Controller-dispatched Muse Job that:

1. creates and validates a marker file;
2. produces a local commit SHA;
3. persists the worker result and task events;
4. releases the capacity claim and records the release lifecycle;
5. leaves GLM routing fail-closed and the OpenAI path unchanged.