# P28 helper checkpoint integration — 2026-10-03

Status: implementation in progress; not deployed or committed.

The guest installer prerequisite is an explicit `helper_verified` checkpoint
between `network_enabled` and `artifact_verified`. Migration 13 preserves old
evidence without fabricating helper completion. Bootstrap has nine stages.

Enabled bootstrap now requires `SESSION_BOOTSTRAP_HELPER_PATH` and an explicit
lowercase 64-character `SESSION_BOOTSTRAP_HELPER_SHA256`. These identify the
trusted controller-side helper snapshot, not an account credential. Disabled
bootstrap rejects partial helper configuration. Production configuration and
databases have not been changed.

Evidence obtained:

- Linux stage-order test first failed because the checkpoint was absent.
- Missing/invalid helper-pin tests first failed, then passed after validation.
- Windows tests passed for `cmd/controller`, `internal/sessionruntime`,
  `internal/store`, and `internal/codexsession` with `-count=1`.
- Windows execution does not validate Linux-only filesystem/SSH tests.
- Subsequent full Windows `go test ./... -count=1` and `go vet ./...` passed.
- Disabled bootstrap explicitly rejects either helper path or helper pin supplied
  alone; targeted rejection tests passed.

Pending review finding: SSH installation must safely create absent exact helper
and launcher-parent directories under verified trusted ancestors; observation
must not create directories. Publication must flush relevant directories before
acknowledgment. The transport implementer owns that correction.

Remaining gates: current-source Linux transport and stage tests, combined
regression and review, coherent commit/push, then controlled production bootstrap
persistence, restart recovery, and account identity/multi-turn acceptance.

Legacy WebCodex OAuth origin restoration remains deferred and is not a P28 gate.

## Linux transport snapshot

Frozen codexsession test binary SHA256:
`6ff7962b0ee5898fd5f9fad282f1c2518f6715716ba3ddf021d0e3ea1dfa6901`.
Local and PVE hashes matched after transfer completed.
All `TestSSHBootstrapHelper` tests passed on PVE using fake SSH that executes
the actual receiver against temporary layouts. Coverage includes absent trusted
directories, install/observe, nonreplacement, malformed receipts, unsafe symlinks,
invalid input, and cancellation. This is not real guest deployment evidence.

Initial execution failed while creating fixtures: PVE `/tmp` is a full 32 GiB
tmpfs. `/var/tmp` has approximately 308 GiB available. The identical binary passed
with `TMPDIR=/var/tmp/p28-linux-validation.sgY9lZcB`, an existing root-owned 0700
test directory. No files were manually removed to address this capacity issue.
Final source changed after this snapshot; fresh final-source Linux validation
is still required before commit.

## Final-source targeted validation

After ancestor-directory synchronization correction, final frozen Linux binaries
matched on PVE: codexsession SHA256
`ddd57179cef120e26a50dbae3694ced0c5927957f7673198f32e5094f3baa903`;
sessionruntime SHA256
`f7c5e312eda48f98a7ce86bdb5d958f61dac76b100ff47829a63a8f3c7343f95`.
All `TestSSHBootstrapHelper` and `TestBootstrapHelper` tests passed using the
existing private `/var/tmp` test directory. Live-cache opt-in was not supplied
in this run and was explicitly skipped. Full Windows tests and vet passed again.
Independent review and complete Linux runtime regression remain required.

Complete Linux sessionruntime regression subsequently returned `PASS` and exit
zero for the same `f7c5e...` frozen binary. The TLS negative-path test emitted
its expected certificate handshake rejection. Independent review remains open.

Controller Linux suite also passed, including actual Linux-only bootstrap factory
assembly without exposing the Session API. Frozen test hash:
`3ad1ea2bafda9e4c054727ff512d3e9f3a0f6776f3ead95eadadda2922d71631`.

Independent review found an order bypass through direct completion of historical
artifact INTENT/UNKNOWN rows. A new regression first reproduced success without
helper evidence. Completion now atomically requires its predecessor COMPLETE in
the SQL UPDATE; transitions to UNKNOWN do not gain that restriction. Full Store,
runtime and Controller tests passed after correction. Scoped re-review and fresh
Linux regression for this Store correction remain pending.
