# P14 publication readiness — 2026-09-20

## Implemented locally

- Added an injected publication boundary for branch push and pull-request
  reuse/create.
- Enforced repository, branch, base-branch, and 40-character commit SHA
  validation before a remote side effect.
- Classified context cancellation/deadline during push or PR operations as
  `UNKNOWN`.
- Added reconciliation that observes an existing open PR and never retries the
  push operation blindly.
- Added a GitHub REST client for open-PR lookup and PR creation. It is not
  wired into the controller by default and tests use an in-process HTTP server.

## Evidence and remaining gate

- `go test ./internal/publication` passed.
- `go vet ./internal/publication` passed.
- No real GitHub token, push, repository, or PR was used in this phase.
- P14 external E2E remains open: disposable repository branch push, PR create,
  PR reuse, and a real timeout/reconciliation observation must be run only after
  the publication credential and target repository are explicitly authorized.

## 2026-09-21 live publication E2E

- Disposable private repository:
  `WilliamLi0623/codex-homelab-p14-e2e-20260921`.
- Branch push succeeded for `codex/p14-publication-20260921` at commit
  `c269ea66a368d675f764e30f5fc2fdf3cf9d385c`.
- Real PR create succeeded as
  [PR #1](https://github.com/WilliamLi0623/codex-homelab-p14-e2e-20260921/pull/1).
- The live `p14live` test then performed a canceled push attempt, classified it
  as `UNKNOWN`, reconciled by observing/creating the open PR, and ran a second
  reconciliation that reused PR #1 without another push or PR creation.
- P14 publication E2E is complete. The live test is gated behind the
  `p14live` build tag and never runs with ordinary unit tests.
