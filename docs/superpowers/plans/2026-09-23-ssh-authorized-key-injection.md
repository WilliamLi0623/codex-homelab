# Homelab SSH authorized-key injection Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Inject the canonical Windows public key into current guests and make
future LXC/VM creation paths inject or inherit it automatically.

**Architecture:** The public key remains sourced from Windows and is uploaded
temporarily to PVE. A reusable PowerShell operation appends it idempotently to
LXC guest authorized-keys files, while stopped templates are updated once so
dynamic clones inherit the key. Bootstrap creates use Proxmox's native
`--ssh-public-keys` and `--sshkeys` options; no Controller scheduler or worker
protocol changes are required.

**Tech Stack:** PowerShell, OpenSSH, Proxmox `pct`/`qm`, LXC, cloud-init,
Go/PowerShell repository tests and live PVE verification.

**Spec:** `docs/superpowers/specs/2026-09-23-ssh-authorized-key-injection.md`

## Global Constraints

- The private key never leaves Windows and never enters Git, Proxmox, a guest, a log, or a fixture.
- Existing `authorized_keys` content is preserved byte-for-byte except for one missing public-key line.
- Each guest's existing authorized-key file is backed up before modification.
- The controller's dynamic VMID, lifecycle, and provider routing code remain unchanged.
- Existing templates, protected LXC100/LXC200, and current worker lifecycle are not destroyed or recreated.

### Task 1: Canonical key helper and bootstrap paths

**Files:**
- Modify: `deploy/bootstrap/Common.ps1`
- Modify: `deploy/bootstrap/stages/30-create-control.ps1`
- Modify: `deploy/bootstrap/stages/40-create-runner.ps1`
- Create: `deploy/proxmox/Inject-AuthorizedKey.ps1`
- Test: PowerShell syntax and dry-run output for the helper

- [ ] Add a helper that reads and validates one OpenSSH public-key line, uploads
  only that line to a temporary root-owned PVE path, and removes the temporary
  file in a `finally` block.
- [ ] Add `--ssh-public-keys <temporary-pve-file>` to the control LXC create
  command and keep VM creation on `qm set --sshkeys`.
- [ ] Make the reusable injection script append the key exactly once to the
  selected LXC root authorized-keys file while preserving old content.
- [ ] Add `-WhatIf`/dry-run support so target selection can be verified without
  guest mutation.
- [ ] Run PowerShell parse checks and dry-run checks before live mutation.
- [ ] Commit the source-only bootstrap change.

### Task 2: Existing guest injection

**Files:**
- Use: `deploy/proxmox/Inject-AuthorizedKey.ps1`
- Modify: `docs/migration/p27-dual-interface-2026-09-22.md`

- [ ] Back up PVE guest configuration and each existing guest authorized-key
  file before modification.
- [ ] Inject into LXC100, 200, 210, 220, 3004, 3005, 3006, 3013, and 3090.
- [ ] Inject into VM101's `webcodex` authorized-key file through QEMU Guest
  Agent, without printing file content.
- [ ] Verify exact key hash, one occurrence, file mode, and preserved backups.
- [ ] Record the target list, backup locations, and verification hashes without
  recording the key itself.

### Task 3: Template inheritance and live creation proof

**Files:**
- Modify: `docs/migration/p27-dual-interface-2026-09-22.md`

- [ ] Verify the active template 3090 contains the canonical key before clone.
- [ ] Create one disposable dynamic worker through the existing Controller
  allocation path, verify its inherited authorized-key hash, and release it
  through the normal lifecycle.
- [ ] Verify the source template and protected guests remain intact.
- [ ] Re-run the injection dry-run to prove idempotence.
- [ ] Commit the evidence documentation and report any remaining guest that
  lacks an SSH server separately from key-file presence.
