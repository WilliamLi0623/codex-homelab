# Local Quota-Routed Codex Session UI Progress

Plan: `docs/superpowers/plans/2026-09-28-local-codex-session-ui.md`
Spec: `docs/superpowers/specs/2026-09-28-local-codex-session-ui-design.md`

## Preflight

- Worktree verified at branch `codex-homelab-v3`, starting commit `123c247f6caaff11a29b74a9cabc8960d9d51795`.
- Tracked files were clean before this plan; existing untracked Go caches, artifacts and binaries are preserved and excluded from commits.
- Plan/spec review confirmed the Controller task console and P25/P26 paths are out of scope.
- Ruling: implement the new session UI without enabling quota fallback until effective subagent route metadata is verified — schema/runtime thread-start probes prove only the main thread configuration, and using fallback before its worker route is proven would violate the approved fixed policy.
- Ruling: use an empty temporary `CODEX_HOME` for safe no-turn probes — the current user `CODEX_HOME` fails App Server SQLite initialization even with scoped write permission; no credentials are copied and no current-home thread is created. Cost if wrong: this leaves a live-runtime concurrency issue unresolved and can delay deployment.
- Native subagent spawn attempt failed with `agent thread limit reached`; implementation continues in the main agent with disjoint task boundaries.

## Task status

- Task 1: partial. CLI 0.156.1 schema and three ephemeral no-turn main-thread routes verified in temporary `CODEX_HOME`; authenticated calls, subagent inheritance, persistent resume and bridge runtime remain open. Fallback gate remains disabled.
- Task 2: complete. `Coordinator.RefreshAndDecide` and tests added; package tests and `go vet` pass. Race validation unavailable because CGO is disabled.
- Task 3: not started.
- Task 4: not started.
- Task 5: not started.
- Task 6: not started.
