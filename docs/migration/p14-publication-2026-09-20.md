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
