# Local Codex Session UI

This is a separate, local-only App Server client at `http://127.0.0.1:8765/codex.html`.
It does not change native Codex Desktop, global `config.toml`, the Controller task UI,
or the P25/P26 worker path. Stopping the process is the rollback; it writes no
global Codex configuration.

> **Experimental:** Live authenticated App Server use has not passed on this host.
> Do not use this runbook for production until the readiness blocker below is resolved.

## Current readiness

The Go/UI behavior is covered by mocks, but the service has not passed an
authenticated live session on this Windows host. App Server startup using the
current user `CODEX_HOME` failed during SQLite initialization. Do not point the
service at a different home or copy credentials as a workaround until that
failure is understood. The normal OpenAI/Luna route is implemented. Automatic
quota fallback remains disabled in production because effective subagent
provider/model/effort has not been proven. If quota is exhausted, session
creation fails closed; this UI does not enable GLM subagent routing.

## Build and launch (PowerShell)

Run from the repository root. Use a new output directory so the existing `ui/dist`
or any other build artifact is not overwritten:

```powershell
$uiDist = Join-Path $env:TEMP ('codex-session-ui-' + [guid]::NewGuid().ToString('N'))
npm --prefix ui run build -- --outDir $uiDist
if ($LASTEXITCODE -ne 0) { throw 'UI build failed' }

$env:CODEX_SESSION_UI_LISTEN = '127.0.0.1:8765'
$env:CODEX_SESSION_UI_STATIC_DIR = $uiDist
$env:CODEX_HOME = 'EXISTING_CODEX_HOME'
$env:CODEX_ROUTING_CONTROLLER_URL = 'https://CONTROLLER_HOST'
$env:CODEX_ROUTING_STATE_FILE = 'ROUTING_STATE_FILE'
# Inject CODEX_ROUTING_STATE_TOKEN from the existing secret manager/environment
# without putting its value in command history or a project file.
go run ./cmd/codex-session-ui
```

Replace `EXISTING_CODEX_HOME` with an authorized Codex home that completes App
Server initialization, `CONTROLLER_HOST` with the host for the existing
routing-state API, and `ROUTING_STATE_FILE` with a private writable path. The
current user home has not passed App Server initialization.

`CODEX_EXECUTABLE` is optional when `codex` is already on `PATH`. The Controller
URL must identify the existing routing-state API. Keep the state file in a
user-private directory. Do not expose port 8765 through a tunnel, LAN bind, or
reverse proxy.

Open `http://127.0.0.1:8765/codex.html` in a browser on the same machine. The
first GET establishes a process-local HttpOnly/SameSite=Strict cookie. The
cookie becomes invalid when the service exits; no API key or local auth token
is entered into the page.

## Stop and rollback

Press `Ctrl+C` in the terminal running `go run`. The HTTP server shuts down and
the App Server child is closed. Native Codex Desktop and unrelated processes
are not stopped. No config rollback is needed. The newly generated UI build
directory can be retained or removed later by its owner; the launch procedure
does not delete it.

The local UI is separate from native Desktop. It selects a route only when
creating a new UI session; it does not make Desktop quota-aware, and it never
switches the provider of an existing thread.
