# P7 provider compatibility recheck — 2026-09-20

The current CC Hub endpoint is the user-provided dashboard host
`https://cch-jp.zenkexi.com/zh-CN/dashboard`; API probes use its `/v1` base
path. The key was read on Proxmox from `/root/cch_jp_key` without printing or
persisting its value.

## Results

`GET /v1/models` succeeded and advertised these relevant IDs:

- `glm-5.3-flash` — current replacement for the former MiMo worker/second-failover role
- `muse-spark-1.3-contributor` and `meta/muse-spark-1.3-contributor`
- `mimo-v2.5` and `xiaomi/mimo-v2.5` (discovery only; not the current routing target)

Minimal `POST /v1/responses` probes with `store=false` returned HTTP 503
`service_unavailable_error` for all three relevant models: GLM-5.3 Flash,
Muse Spark 1.3 Contributor, and MiMo v2.5. The responses contained no model
output or completed response ID.

## Routing decision

This is classified as a CC Hub gateway outage, not as a model-specific
failure. GLM-5.3 Flash remains the canonical replacement in configuration, but
both CC Hub routes remain disabled until a fresh completed Responses probe
succeeds. No provider failover or authenticated P13 task was started from this
probe.
