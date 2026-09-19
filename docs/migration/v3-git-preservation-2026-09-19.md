# V3 Git preservation — 2026-09-19

## Scope and result

Both local worktrees were clean at inspection time: no untracked files and no
uncommitted modifications required discard or patch preservation.

| Ref | Commit at inventory | Preservation action |
| --- | --- | --- |
| `main` | `729afde` | Push the committed worktree-ignore safeguard. |
| `codex-homelab-v3` | `7a4cf28` | Push the V3 freeze implementation before architecture replacement. |

The V3 work happens in a linked worktree under `.worktrees/`, which is now
ignored by Git. This protects the main checkout while preserving all source
changes as commits on the V3 branch.

## Intentional exclusions

No source changes, private credentials, bootstrap runtime state, logs, or
recovery assets are added to Git. Existing ignore rules continue to exclude
those secret-bearing or machine-local paths.

