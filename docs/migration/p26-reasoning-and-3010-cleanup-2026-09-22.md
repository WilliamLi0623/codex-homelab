# P26 reasoning routing and LXC3010 cleanup — 2026-09-22

## LXC3010

The explicitly authorized cleanup targeted only VMID `3010` and its matching
P26 recovery probe:

- task: `task-f3a2c20046befdb972f8ac3d8840efb4`
- attempt: `attempt-12e83bbbb68bb282bbbf8edd95a191d7`
- Kubernetes Job: `task-f3a2c20046befdb972f8ac3d8840efb4-d62be78c5d1fd5d8`
- Kubernetes Node: `codex-lxc-3010-sha256-806fd7c48de5bd46101f7c65afa2e4ea2f8d7df40`

The Pod was pending with `ImagePullBackOff`, so no worker side effect or
commit was observed. The exact Job and Pod were deleted, the Node was cordoned
and removed after stopping its still-connected k3s agent, and VMID 3010 was
stopped and destroyed with purge. Verification showed no LXC config, rootfs,
matching Job, matching Pod, or matching Node remained.

The Controller SQLite file was backed up before the narrow ledger cleanup:
`/var/lib/codex-controller/controller.sqlite.pre-p26-3010-cleanup.bak`.
The attempt and execution handle are `CANCELLED`; the claim row remains as a
historical capacity ledger entry and no false release-completed record was
created. Pending execution-spec enumeration now excludes terminal attempts so
cancelled work cannot be re-observed after restart.

## Explicit reasoning routing

The routing contract is now explicit and fail-closed:

| model/profile | wire form | reasoning setting |
| --- | --- | --- |
| `muse-spark-1.3-contributor` | Responses | `reasoning.effort = xhigh` |
| `glm-5.3-flash` | Chat Completions | `reasoning_effort = max` |
| OpenAI profiles | existing path | unchanged |

The worker Job manifest carries `CODEX_MODEL_REASONING_EFFORT` only when a
configured Muse/GLM profile selects it. The agentd adapter maps the setting to
the correct request field and rejects the opposite value for the exact model.

The user-level Muse profile contains `model_reasoning_effort = "xhigh"` and
was backed up before editing. The global OpenAI configuration remains on
`gpt-5.6-luna` with `model_reasoning_effort = "high"`.

## Verification

- targeted Muse, K3s runtime, agentd, Controller, and store tests passed;
- Controller-only rerun passed after a full-suite Windows temporary-file-lock
  failure;
- full suite passed for all packages except one flaky
  `cmd/controller` cleanup race, then the isolated rerun passed;
- updated Linux Controller binary was installed in LXC210 and `/v1/ready`
  returned HTTP 200;
- live CC Hub probes at the end of this update returned HTTP 503 `error code:
  1010` for both plain and reasoning-bearing Muse/GLM requests, so upstream
  acceptance of the new reasoning fields remains unverified while the local
  request/configuration path is covered by tests.
