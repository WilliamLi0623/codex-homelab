# Self-hosted task UI

The self-hosted operator interface is a static React application served behind
a private HTTPS or Tailscale reverse proxy. The browser talks only to the UI
backend-for-frontend (BFF); the BFF talks to the existing Controller.

```text
browser
  -> private HTTPS/Tailscale proxy
  -> static UI
  -> task-ui-bff 127.0.0.1:8081
  -> Controller /v1
```

The BFF is a narrow, allowlisted proxy. It does not become a scheduler, task
database, model router, or replacement for the Controller. It forwards task
queries, event history/stream requests, messages, and the existing guarded
attempt operations. It limits request bodies to 1 MiB, redacts upstream error
bodies, forwards `Last-Event-ID` for stream resume, and rejects public listener
addresses by default.

## Build and local development

From `ui/`:

```text
npm install --cache .npm-cache
npm test -- --run
npm run build
npm run dev
```

The Vite development server proxies `/ui/api` to `http://127.0.0.1:8081`.
Production deployment can set `TASK_UI_STATIC_DIR` so the BFF serves the
generated `ui/dist` directory as static files, or use another private static
file server. The BFF API remains separately authenticated.

## BFF configuration

The service reads these environment variables (command-line flags take
precedence):

```text
TASK_UI_LISTEN=127.0.0.1:8081
TASK_UI_CONTROLLER_URL=http://127.0.0.1:18080
TASK_UI_AUTH_TOKEN=<operator token>
TASK_UI_CONTROLLER_TOKEN=<optional Controller service token>
TASK_UI_STATIC_DIR=/var/lib/codex-task-ui
```

`TASK_UI_AUTH_TOKEN` is required. The browser must not receive this token. A
private reverse proxy or an authenticated operator gateway should inject the
BFF `Authorization: Bearer ...` header for an authorized request. The static
UI itself uses same-origin `/ui/api` requests and never embeds a secret.

The systemd unit runs as `codex-ui`, binds the BFF to loopback, applies process
hardening, and explicitly unsets unrelated infrastructure/provider secrets.

## Live updates and safety

The UI subscribes to the Controller event stream with a cursor. It resumes
using `Last-Event-ID`, suppresses duplicate event IDs, and backs off briefly
after a transient disconnect. Event history remains authoritative in the
Controller store.

Continuation requests use the existing durable idempotency key and preserve
`UNKNOWN` delivery outcomes. The UI must show an ambiguous outcome as needing
reconciliation; it must never retry a side-effecting request merely because a
network response was lost. Destructive or infrastructure-changing controls
remain guarded by the Controller and are not implemented by the BFF.

## Rollback

To roll back this interface, stop and disable the new static UI/BFF deployment
and leave the existing Controller, MCP gateway, OpenAI route, and V3 worker
path unchanged. No task or provider state is stored by the UI layer.
