# P6 CC Hub discovery

This record contains no credential, host, path, dashboard URL, or response
body. Credentials remain in private runtime configuration.

## Model discovery

Authenticated `GET /v1/models` completed with HTTP 200. The exact discovered
model IDs include:

- `muse-spark-1.3-contributor`
- `mimo-v2.5`
- `xiaomi/mimo-v2.5` (alias; canonical route uses `mimo-v2.5`)
- `glm-5.3-flash` (current low-cost worker replacement for MiMo)

## Minimal Responses probes

The minimal no-repository prompt was `Reply exactly OK.` with a small output
limit and `store: false`.

| Profile | Model ID | Result | Routing state |
| --- | --- | --- | --- |
| Muse failover | `muse-spark-1.3-contributor` | HTTP 200 with a response ID | discovered; requires P7 completion validation |
| GLM Flash worker | `glm-5.3-flash` | Chat Completions/tool-call compatibility verified; adapter still pending | disabled; fail closed |

GLM-5.3 Flash is the current replacement for the former MiMo worker position.
It uses coding-agent/Chat Completions semantics and remains disabled until the
production adapter and worker-task gate pass.
