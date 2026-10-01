# P28 Task 4 — new-clone bootstrap feasibility

Status: **bootstrap without Codex account auth passed; inherited infrastructure secrets block real Session use**.
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
