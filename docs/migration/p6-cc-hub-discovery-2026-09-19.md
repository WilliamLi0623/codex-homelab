# P6 CC Hub discovery

This record contains no credential, host, path, dashboard URL, or response
body. Credentials remain in private runtime configuration.

## Model discovery

Authenticated `GET /v1/models` completed with HTTP 200. The exact discovered
model IDs include:

- `muse-spark-1.3-contributor`
- `glm-5.3-flash`

## Minimal Responses probes

The minimal no-repository prompt was `Reply exactly OK.` with a small output
limit and `store: false`.

| Profile | Model ID | Result | Routing state |
| --- | --- | --- | --- |
| Muse failover | `muse-spark-1.3-contributor` | HTTP 200 with a response ID | enabled for later P7 validation |
| GLM Flash worker | `glm-5.3-flash` | HTTP 503 `no_available_providers` | disabled; do not route tasks |

The GLM profile remains recorded so a later P6/P7 re-probe can safely enable
it only after a successful compatibility result. This replaces the superseded
MiMo role in V3 routing.
