# Homelab SSH authorized-key injection

## Goal

Use the existing Windows `id_ed25519.pub` as the single public-key source for
current and future Proxmox guests without copying or storing its private key.

## Scope

Existing LXC guests receive the key in `/root/.ssh/authorized_keys`. The
existing VM101 guest receives it for the currently verified `codex` account;
future VM bootstrap continues to use the configured `webcodex` cloud-init
user. The stopped LXC templates 3900–3902 used for future clones receive the
same key, so dynamic `pct clone` workers inherit it. Legacy 3004/3005/3013/
3090 remain privileged rollback resources and are reserved from allocation.

The bootstrap path for a newly created control LXC passes the key to
`pct create --ssh-public-keys`. The VM bootstrap path continues to use
`qm set --sshkeys` for cloud-init. All injections are append-only and
idempotent.

## Safety invariants

- The private key never leaves Windows and never enters Git, Proxmox, a guest,
  a log, or a fixture.
- Existing `authorized_keys` content is preserved byte-for-byte except for one
  missing public-key line.
- Each guest's existing authorized-key file is backed up before modification.
- The public key is verified by SHA-256 fingerprint before and after injection.
- Template networking and K3s services are not changed by key injection.
- The controller's task/lifecycle/provider routing semantics remain unchanged;
  the dynamic VMID boundary is 3000–3899 and worker inheritance comes from
  the active 3900–3902 templates.

## Acceptance

- All current LXC targets contain the canonical key exactly once.
- VM101's current `codex` authorized-key file contains the canonical key
  exactly once; future bootstrap remains explicitly configured for `webcodex`.
- Bootstrap source contains explicit LXC and VM key injection paths.
- The active worker templates contain the key, and a disposable clone inherits
  it without manual post-clone editing.
- Re-running the injection is a no-op for guest key content.
