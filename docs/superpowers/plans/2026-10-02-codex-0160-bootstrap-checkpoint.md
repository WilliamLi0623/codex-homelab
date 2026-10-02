# Codex CLI 0.160.0 bootstrap checkpoint

Date: 2026-10-02
Plan context: [Local quota-routed Codex Session UI](2026-09-28-local-codex-session-ui.md)

## Result

- Official Codex GitHub Latest was checked and resolved to `rust-v0.160.0` (released 2026-10-01): <https://github.com/openai/codex/releases/latest>.
- The Linux `x86_64-unknown-linux-musl` release archive passed the publisher's `codex-package_SHA256SUMS` check. The extracted package contains 44 regular files and no symlinks; the pinned manifest matches every file after normalizing executable/data permissions to the installer contract.
- On Proxmox, the official CLI binary reported `codex-cli 0.160.0`. A no-credential/no-model App Server `initialize` handshake returned a valid JSON-RPC result and exited successfully.
- The Linux installer was exercised on Proxmox against the actual official release tree, writing only under the isolated `/var/tmp/p28-codex-0.160.0-20261002/test-tmp` test root. Install evidence and subsequent read-only observation matched; the installed executable SHA-256 matched the pinned manifest. No `/opt/codex`, `/usr/local/bin/codex`, LXC, template, Controller service, or production bootstrap setting was changed.
- Bootstrap selection now defaults to pinned `0.160.0`, while explicit `0.155.0` remains available for rollback. Mutable values such as `latest` are rejected. The existing disabled-bootstrap path ignores the otherwise-unused version override.

## Verification

- Linux/PVE integration probe: both official-manifest verification and install/observe tests passed (`sessionruntime-r5.test`, `PASS`).
- `go test ./... -count=1 -timeout 240s`: passed.
- `go vet ./...`: passed.
- `git diff --check`: passed.

## Still open

- This updates the pinned artifact used by a future enabled bootstrap. Production bootstrap remains disabled/unconfigured; the 0.160.0 package has not been staged into LXC210's production cache, installed into templates, or rolled out to guests.
- P28's persistent per-runtime App Server lifecycle and Controller/API integration are still unfinished. This checkpoint does not imply P28, production P25/P26 isolation, or quota fallback is complete.
- The user's requested GitHub push remains pending the full outgoing-history, changed-blob, and secret audit; this checkpoint is not authorization to include unrelated dirty files or untracked build/cache artifacts.
