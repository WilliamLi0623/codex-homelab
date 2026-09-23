# P7 provider compatibility recheck — 2026-09-20

The current CC Hub endpoint is the user-provided dashboard host
`https://cch-jp.zenkexi.com/zh-CN/dashboard`; API probes use its `/v1` base
path. The key was read on Proxmox from `/root/cch_jp_key` without printing or
persisting its value.

## Results

`GET /v1/models` succeeded and advertised these relevant IDs:

- `glm-5.3-flash` — current replacement for the former MiMo worker/second-failover role
- `muse-spark-1.3-contributor` and `meta/muse-spark-1.3-contributor`
- `mimo-v2.5` and `xiaomi/mimo-v2.5` — canonical MiMo ID and alias

The current compatibility result is protocol-specific. `glm-5.3-flash` is
coding-agent-only, so `/v1/responses` is not a valid GLM compatibility route.
A live `/v1/chat/completions` request returns HTTP 200 and an explicit function
probe returns a tool call with the requested function name. `/v1/messages`
remains 503. The verified GLM wire contract is OpenAI Chat Completions with
tool calls; the local adapter is still required before production enablement.

## Routing decision

GLM is configured only as a worker candidate with `wire_api=coding-agent`.
The raw endpoint is verified, but the local adapter is not implemented, so the
worker path remains fail-closed. Full-coding remains OpenAI then Muse.
