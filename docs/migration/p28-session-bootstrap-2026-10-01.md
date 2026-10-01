# P28 Task 4 — new-clone bootstrap feasibility

Status: **bootstrap without Codex account auth passed; approved inherited-credential paths cleaned; production readiness remains gated**. The sanitation addendum below supersedes the original retained-secret stop gate for the exact approved targets, not for the entire image.
Date: 2026-10-01 (client Asia/Taipei). Baseline: `codex-homelab-v3`, `ab722e0`.

## Approved scope and actual boundary

The user confirmed a new clone for independent SSH identity and verified Codex installation. Existing LXC4000, frozen templates, worker/controller services, Windows private keys and Codex account credentials were outside the mutation scope. No destructive cleanup was performed.

Stateful PVE clone/config/start/console requests used the independent `codex-sessions@pve!session-v1` token, read only in remote process memory from LXC210's existing private environment file. TLS verified the PVE CA and certificate hostname. Root SSH to PVE was used to launch diagnostic helpers, inspect host isolation, and stage an already-approved binary; it was **not** substituted for API permission checks. This is not yet production Controller-process bootstrap integration.

```text
PVE diagnostic helper → restricted Session-token HTTPS / console → new LXC4001
                                        │ non-secret public host key
                                        ▼
LXC210: per-instance client private key + pinned known_hosts
    → strict SSH → LXC4001 → Codex CLI / App Server stdio
```

This is a fixed-target, operator-authorized spike, not a public arbitrary-command API, deployed bootstrap service, Store-bound runtime or second capacity allocator.

## Verified matrix

| Check | Observed result |
| --- | --- |
| Global VMID gate | 4001 was free before exactly one clone POST; PVE later correctly reported it occupied |
| Clone authority | Session token clone task completed `OK`, source 3900, full clone, destination pool `codex-sessions` |
| Root disk | `local:4001/vm-4001-disk-0.raw,size=16G` on SSD |
| Workspace | `pool:subvol-4001-disk-0,mp=/workspace,size=8G`; guest reports ZFS |
| Initial isolation | `link_down=1`, host `veth4001i0` DOWN without bridge membership; PVE guest netin/netout both zero while fenced |
| Identity | UID 0; actual hostname `codex-session-4001-bootstrap`; privileged clone; `onboot=0` |
| Worker startup | Template contained enabled `k3s-agent`; only the clone's service was disabled, stopped and masked before networking |
| New SSH host identity | New ed25519 key selected as the sole effective sshd host key; public identity retrieved through authenticated console before networking |
| Client identity | Fresh per-instance ed25519 private key generated and retained only on LXC210, root/0600; only public key installed in guest |
| Pin order | `known_hosts` written on LXC210 before link enabled; no TOFU or `accept-new` |
| Strict SSH | LXC210 → 4001 succeeded with explicit identity, `StrictHostKeyChecking=yes`, `IdentitiesOnly=yes`, `-F /dev/null` and per-instance HostKeyAlias |
| Negative pin check | Unknown alias with the same pin file failed with exit 255 / host-key verification error; actual mismatched-key and reboot tests remain unperformed |
| IP discovery | Session-token `/interfaces` API found DHCP `10.58.2.91`; this address is a snapshot, not a static allocation |
| Codex | `codex-cli 0.155.0`; installed binary SHA-256 below matched source, transport staging and installed guest |
| App Server | Through LXC210 pinned SSH, matching `initialize` response, then `initialized`, `account/read`, and empty `thread/list`; stdin remained open through responses |
| Authentication | `account` was null; no `/root/.codex/auth.json`; no login or model turn |
| Process shutdown | App Server closed after probe; final guest process inspection found no remaining `codex` process |
| Existing state | Template3900 and diagnostic4000 config hashes unchanged; no production Controller binary deployment/restart, ACL broadening, Session API or routing enablement |

Codex binary:

```text
660e159a49e823ac8e5986cb238f73158ce4b957d40d9292f8de90862644b501
```

Source was only the existing LXC3006 binary at `/opt/codex-0.155.0-v3-install-20260920-final2/vendor/x86_64-unknown-linux-musl/bin/codex`, whose expected digest was already recorded in `p22-p25-runtime-2026-09-22.md`. No auth/config/home was copied. This proves integrity against the approved existing artifact, not fresh independent vendor signature verification. Other bundled tools and real coding execution remain untested in this clone.

Final sanitized App Server result:

```json
{"vmid":4001,"version":"0.155.0","appserver_initialize":true,"account_authenticated":false,"history_empty":true,"healthy_after_rpc":true,"installed_sha256":"660e159a49e823ac8e5986cb238f73158ce4b957d40d9292f8de90862644b501"}
```

## Failures and safe reconciliation

1. Clone completed successfully, but the first helper's exact description check rejected PVE's appended newline. Read-only config/task checks established ownership and stopped state. A separate guarded configure step continued from that checkpoint; clone was not replayed.
2. The first read-only console inspection timed out. A subsequent bounded read-only probe returned complete UID/hostname/service/network evidence. The original transient cause is **unresolved**; do not label it fixed or infer safe mutation retries from this result.
3. Identity setup generated new keys, stopped/masked worker service and stopped SSH, then `sshd -t` failed because stopping the service removed `/run/sshd`. Read-only inspection established exactly those completed effects while the NIC stayed fenced. A distinct `finish-identity` operation checked the existing key and authorized public key, created the missing runtime directory, validated/started sshd and pinned its public identity. Key rotation/client-key generation were not replayed.
4. Independent review found Python `assert` safety gates inappropriate for production. Later console/SSH/App Server drivers explicitly reject optimized Python, but this is a diagnostic mitigation, **not** a production-grade guard implementation. Early clone/config helpers were invoked without optimization and need unconditional typed checks before reuse.

## Credential-isolation stop gate

The clone has no Codex account credentials, but it is **not infrastructure-secret-free**. A metadata-only inspection found `/etc/systemd/system/k3s-agent.service.env` with field names `K3S_TOKEN` and `K3S_URL`; no values were printed. A subsequent path-only inventory also found `/etc/rancher/node/password` and inherited private keys/kubeconfigs under `/var/lib/rancher/k3s/agent`, including kubelet, kube-proxy and k3s-controller material. `/root/.kube` was absent. Masking the service prevents joining K3s but does not remove those secrets from a future full-access agent's filesystem. The initial path listing was bounded and truncated among container image files; it is not an exhaustive whole-image credential audit.

Inherited SSH host private keys remain in the cloned filesystem and the root-only SSH backup. They are not the keys served by sshd or used for trusted connections, but they must not be exposed to a real Session agent. No credential/private-key deletion or overwrite cleanup is authorized by this spike; retained backups cannot be treated as sanitized just because they are root-only inside a privileged full-access guest.

**Do not inject account auth, submit a model turn, expose this guest as a Session, or promote READY until an exact credential inventory and authorized clone-only sanitization have passed.** A production path must sanitize all inherited worker secrets/shared private keys before account use, or use a separately approved clean immutable Session image; frozen templates remain untouched. Do not assume the environment file is the only inherited secret.

Task 4 also still needs reviewed/tested production bootstrap integration, post-restart pin verification, mismatch-key rejection, account/epoch identity readiness, and the full persistent lifecycle/replacement/reconciliation gate. Task 5 live authenticated acceptance is not passed. `CheckReady` remains fail-closed; Session API remains unavailable; automatic failover/failback remains disabled.

## Retained state and recovery evidence

LXC4001 is running, DHCP, in `codex-sessions`, `onboot=0`, SSH active, k3s-agent masked, no Codex account auth or active App Server. It is retained as a diagnostic spike, not a ready Session. Do not recreate or delete it automatically.

- PVE source/clone config backups: `/var/tmp/p28-bootstrap-4001-kxxb57vj`, `/var/tmp/p28-bootstrap-4001-config-ckfg8v42` (root-only).
- Guest original SSH configuration/private keys and moved worker unit: `/root/p28-bootstrap-4001-backup` (root-only, sensitive).
- Controller-side per-instance identity/pin and staged binary: `/var/lib/codex-controller/session-bootstrap-spikes/4001-20261001` (root-only, sensitive private key retained).
- PVE binary staging: `/var/tmp/p28-codex-4001-eb7irmwb`.
- Helpers are retained at their distinct `/var/tmp/*4001*20261001*` paths; the final fixed console binary is `/var/tmp/console-4001-finish-20261001`, SHA-256 `f229ddea3f25debf413e65533c02ba69709c96d7809af248d8024a05e66418af`.
- Existing template3900 config SHA-256: `bafe51301f727e8308fd27c9b02ee71629b8c008f263944bccd0000553edc316`.
- Existing4000 config SHA-256: `48845fa1220a52d51b75b704f46f0b3879e12b978377863ca24718a8ef8b8a56`.

Cleanup requires a non-destructive exact-target inventory and immediately prior explicit confirmation. None of these directories, keys, volumes or guests was deleted.

## Source regression

Fresh `go test ./internal/sessionruntime ./internal/codexsession -count=1` and `git diff --check` passed. Go emitted the existing Windows telemetry-cache permission warning but exited zero. No production source was changed; scratch diagnostics are not shipped code. The three pre-existing tracked Windows Session UI modifications were preserved.

## Approved clone-only sanitation — 2026-10-01

The user explicitly confirmed the proposed backup and deletion. A fresh non-destructive preview verified ownership of running LXC4001, all ten targets existed without root symlinks, and no target contained a live mount. The fixed-target helper used unconditional guards, required the clone hostname/pool/rootfs/workspace metadata, no Codex/k3s process or account auth, and masked k3s-agent. It was not a production bootstrap adapter.

Before deletion, all ten targets were streamed directly from guest tar to a PVE-host backup; private material never passed through Windows or stdout. The PVE directory is `/var/tmp/p28-sanitize-4001-svgxne67` (root/0700); `inherited-sensitive-state.tar` and `manifest.json` are root/0600. The tar metadata was parsed, all approved roots were present, and every archive member was within those roots (34,227 members). Backup SHA-256:

```text
5ee1e906b4f6d6c72340197b180c0a0e645a20918336d6c6576813f7e19cb010
```

Deleted **only inside LXC4001**, after that backup:

- `/etc/rancher`
- `/var/lib/rancher/k3s`
- `/etc/systemd/system/k3s-agent.service.env`
- `/root/p28-bootstrap-4001-backup`
- `/etc/ssh/ssh_host_rsa_key` and `/etc/ssh/ssh_host_rsa_key.pub`
- `/etc/ssh/ssh_host_ecdsa_key` and `/etc/ssh/ssh_host_ecdsa_key.pub`
- `/etc/ssh/ssh_host_ed25519_key` and `/etc/ssh/ssh_host_ed25519_key.pub`

Postconditions verified every exact target absent, new P28 host key/private and public files, sshd drop-in, authorized client public key, and Codex binary byte-identical; `/workspace` device/inode unchanged; sshd config valid and SSH active; no account auth file. PVE configs for3900,4000,4001 remained byte-identical. No guest/volume was destroyed, no frozen template or other guest file was cleaned, and the original controller identity/pin on210 was untouched. The removed material can be recovered from the sensitive PVE backup; archive extraction/restoration was **not** rehearsed and must be separately authorized, not automatic.

Fresh post-cleanup verification through LXC210 passed pinned SSH and missing-pin rejection, plus App Server initialize/initialized, `account=null`, empty thread history and healthy RPC transport with the same Codex hash. No login or model request was performed. The prior guest-local SSH/worker backup path in the retained-state section is now absent; its contents are recoverable only from the external archive. Earlier “no cleanup authorized” statements describe the pre-confirmation checkpoint, not current authorization/state.

A separate real **mismatched-key** probe also passed: it read only the old PUBLIC ed25519 key from the external archive, wrote an exclusive independent `known_hosts.mismatch-probe-20261001` fixture on210, and required SSH exit255 plus host-identification-changed and verification-failed errors. The correct pin/client identity was untouched and no command executed through the rejected connection. This supersedes the earlier unperformed mismatched-key gate; reboot/pin-persistence remains unverified. The public-only fixture is retained, not silently deleted. External backup readback verified directory root/0700, archive root/0600 and archive size2,678,947,840 bytes.

This resolves the **known exact inherited-secret paths**, not a forensic whole-image credential audit or logical erasure of deleted disk blocks. The PVE backup deliberately contains secrets and is kept outside the future Session filesystem; do not copy it back to the agent guest or browser. Full image provenance/sanitation and reviewed production bootstrap/readiness integration still need validation before real account use. Task4 lifecycle and Task5 authenticated acceptance remain open; no READY promotion, Session API or automatic routing was enabled.

## Diagnostic reboot and source account baseline — 2026-10-01

One Session-token reboot of exact diagnostic LXC4001 was submitted only after ownership/configuration, no pending changes, no account/Codex process, masked k3s, sanitation and pin/key/workspace preflight. An exclusive intent was persisted before submission under external root-private `/var/tmp/p28-reboot-4001-095vjf_y`; intent was never reused to retry the mutation. A changed guest startup was observed (LXC boot IDs can be host-shared). Postconditions verified unchanged guest/client keys, Controller known_hosts pin, workspace device/inode, and PVE configurations for3900/4000/4001.

After restart, LXC210 strict pinned SSH and missing-pin rejection passed. Codex0.155.0 App Server initialize, account-null, empty thread history and healthy RPC passed again with the recorded binary hash. No production Controller restart, account login or model turn occurred. This closes diagnostic reboot/pin persistence, not production bootstrap or authenticated restart acceptance. Sensitive snapshots remain outside the guest and are not copied into this repository.

The user selected LXC3006's currently logged-in account as Codex A. A bounded Linux App Server `account/read` with `refreshToken=false` reported `type=chatgpt`, `planType=plus`, and fields `email/planType/type`. Raw email and token values were neither printed nor persisted here; no credentials were copied. This is a source-only cached identity baseline, not proof of current model entitlement, stable workspace/account-ID matching, or authentication in4001. Codex B remains unused.

## Publication checkpoint

Read-only GitHub inspection found `origin/codex-homelab-v3` at `331d587da07e27027bbea52c08043bbd65fb384b`; local baseline1888bf1 was144 commits ahead, spanning286 files. No push occurred. A value-redacted heuristic scan across those144 commits found no matching candidate paths after correcting a false-positive `sk-` substring pattern. It is not a standard secret scanner or publication clearance: encoded/binary secrets and the complete publication scope still require review. No release tag is justified by this probe.

## Reviewed durable bootstrap source slice

Store migrations9–11 add generation-bound seven-stage checkpoints. Only a fresh atomic claim permits Apply; existing INTENT/UNKNOWN can only be observed, never replayed. Evidence is a lowercase SHA256 digest only, empty until completion. Database constraints/triggers reject noncomplete evidence and completed-row changes, including SQLite `INSERT OR REPLACE` with recursive triggers default/OFF/ON. Repeated Begin returns existing checkpoints without new authority.

The injected coordinator keeps each guest action separate from read-only reconciliation. Optional Manager integration invokes Ensure only for newly created fenced guests; resume/reconcile require existing checkpoints and never bootstrap missing phases. Runtime/account `CheckReady` remains mandatory. Canceled bootstrap uses a bounded uncanceled context only to persist the ambiguous binding; it cannot continue guest actions. A storage failure can still leave a stage INTENT rather than UNKNOWN, which remains ambiguous and cannot authorize replay. No real production driver or account provisioning was implemented in this slice, and no existing database or service was migrated/deployed.

Independent review identified and then cleared the evidence/REPLACE blockers. Fresh main `go test ./... -count=1`, `go vet ./...`, and `git diff --check` passed. Go's telemetry upload-token permission warning did not affect exit status. Frozen Linux suites ran in the existing isolated temporary directory after SCP completed and both hashes matched; each full suite passed `-test.count=3`:

| Linux test | SHA256 |
| --- | --- |
| Store | `dcd3db1c08d8c19450396c99207ef9eb142453518411f0b6dd430381983d422d` |
| Session runtime | `d05d105d421cb6f29a03ef1e654354a79dd7d7ace877172eacee624f4c646c1b` |

Artifacts remain under `/var/tmp/p28-console-regression.95l0Hq/`. These tests use isolated SQLite databases and mock transports, not production guest operations. An initial remote command failed Python parsing because of PowerShell quoting; it executed neither tests nor file changes. The corrected stdin script succeeded. Expected negative TLS fixture warnings occurred without test failure. Race detection remains unverified because this environment has cgo disabled.

## SSH material and App Server transport source validation

The frozen SSH material Linux binary SHA256 `3502a0ee2cf8b931f3d021253d2d65a8bcbcc5c70a9c9c942ff512c69ed0f995` passed all `TestSSHMaterial` tests on PVE. This includes valid host-pin substitution rejection, private/public/manifest tampering, symlinks, writable ancestors, generation continuity, failed keygen without retry, and partial pin rejection. The upload had mode0644 rather than executable; a separate mode0700 execution copy retained identical bytes. No guest or production service was modified.

Material fixes add parent-directory fsync before keygen, current-UID/root ownership and non-sticky writable ancestor rejection, an exclusive pin digest, and post-write pin readback. Trusted console provenance and network ordering remain production-driver responsibilities; this library alone cannot prove them.

Pinned SSH transport Linux binary SHA256 `e6db39be5d915dffaf98acf9580f4336dc2450d245c688e37abf965e11cd8118` passed the command-validation, POSIX path checks and fake SSH subprocess lifecycle tests. Explicit identity/pin/alias, no agent/password/forwarding, sanitized environment and EOF shutdown are covered. Windows package tests and vet passed; a subsequent full `go test ./... -count=1` and `go vet ./...` also passed. The existing Go telemetry permission warning remains unrelated to test exit status. Real production driver integration, account readiness and A/B isolation are still open.

Read-only live reinspection found the existing8007 proxy and10.58.2.187:8006 upstream each returning HTTP200. The proxy unit already names that upstream, and the PVE allowlist includes the host address. Historical screenshot worker IDs3011–3026 were absent except the retained stopped legacy template3013. No cleanup or proxy change was performed. The first K3s inventory command used an unavailable `kubectl` entry and suppressed its diagnostics; its empty output is not proof of an empty cluster and requires a corrected `k3s kubectl` check.

The next source prerequisite is `VerifyBootstrapIsolation`: one read-only configuration snapshot validates generation ownership, SSD root, HDD workspace, console mode, default-zero startup/privilege fields and every NIC's DHCP/fence options. It returns a digest of selected non-secret fields. It does not establish packet-level fencing or authorize future mutations. Focused source tests and vet pass. Frozen Linux binary SHA256 `6a452c482546108a869e5abfa26956b4d6f517dbd99a7eb3dff7a0da44f143c1` passed `TestBootstrapIsolation` and `TestSSHMaterial` after completed-transfer hash verification. Independent review approved this config-level prerequisite. Fresh combined full Go tests and vet pass after the Linux-only SSH launch restriction. Production READY, Session API, account use and automatic switching remain gated.

Independent SSH review cleared the Linux material/transport slice, with one important finding about unsupported Windows launch. `StartSSHAppServerProcess` now rejects non-Linux hosts before starting any process; a Windows regression test passes and the scoped rereview approves this restriction. Portable command-construction tests do not claim Windows runtime/ACL support.

Corrected live `k3s kubectl` inventory shows3006 and the control node Ready, plus an old NotReady3017 Node whose LXC is absent. Historical completed/failed Jobs and two old P13 Jobs reporting Running remain. These are observations requiring Controller/Pod reconciliation, not authority to delete or replay work. LXC4001 remains running. No Node/Job mutation was performed.

## Diagnostic image audit continuation

Fresh metadata-only inspection of4001 found no account auth file, K3s/rancher credentials, root cloud credential directories, Git credential file or netrc at the previously enumerated paths. `k3s-agent` remains masked and SSH active. Root SSH contains only the authorized public key file. A bounded private-key-header path scan across `/etc`, `/root`, `/home`, `/opt`, `/usr/local` and `/var/lib` found the served per-instance P28 host key and `/etc/ssl/private/ssl-cert-snakeoil.key`; no private key contents were emitted. The default TLS key's provenance/reuse remains unresolved. This scan does not detect arbitrary binary/encrypted stores or credentials outside those paths and is not whole-image clearance.

Effective sshd uses `/etc/ssh/ssh_host_p28_ed25519_key` with password authentication disabled. The public path is `/etc/ssh/ssh_host_p28_ed25519_key.pub`; production console probes will use this existing spelling. `/root/.codex` contains runtime databases and an installation ID created by the earlier diagnostic App Server probes. No model turn or account login was performed in this continuation.

The live PVE configuration-update schema explicitly supports a SHA1 `digest` precondition to reject concurrent configuration changes. The production network-enable adapter should use this together with fresh ownership/fencing checks and readback; a prior isolation digest alone does not authorize enabling a NIC.

`bootstrapNetworkEnableForm` now constructs that compare-and-set patch without issuing any requests: a valid40-character lowercase SHA1 digest is mandatory, every NIC must already be fenced/DHCP with unique options, and the patch preserves all NIC parameters except setting `link_down=0`. Focused rejection/preservation tests, the Session runtime suite and vet pass. It remains an unconnected helper; production must enforce durable completed sanitation/host-pin prerequisites and readback around a single mutation.

Further live Pod inspection establishes that the old P13 Jobs' actual Pods are Pending on3006, rather than executing model work. The absent-guest3017 Node retains `wrangler.cattle.io/node` finalizer and only standard node labels. Controller `/v1/health` returned `status=ok`. This narrows the stale-resource investigation but does not close cleanup/reconciliation acceptance or authorize deletion.
