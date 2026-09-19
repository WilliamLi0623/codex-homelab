# P7 provider compatibility

This report records only model IDs, HTTP status, and sanitized protocol state.
It contains no credential, key host, key path, API base URL, dashboard URL, or
response body.

## CC Hub probes

Each probe used a fixed no-repository prompt, `store: false`, and an output
budget of 16 or 64 tokens.

| Profile | Model ID | Evidence | Decision |
| --- | --- | --- | --- |
| Muse failover | `muse-spark-1.3-contributor` | HTTP 200 and response ID, but status `incomplete` with zero output items at both output budgets | disabled |
| GLM Flash worker | `glm-5.3-flash` | HTTP 503 with `no_available_providers` | disabled |

Neither CC Hub profile is permitted in automatic routing until a fresh P7
probe yields a completed response with observable output. The configured
OpenAI/Codex primary remains separate; its account-specific compatibility has
not been asserted by these CC Hub probes.
