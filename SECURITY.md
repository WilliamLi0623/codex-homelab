# Security

Never commit credentials, private hostnames, private domains, internal IPs, tunnel IDs, personal usernames/emails, SSH private-key paths, browser profiles, Codex auth files, cookies, PATs, or .env secret values.

Runtime credentials belong outside the repository with restrictive filesystem permissions.

Before release, run gitleaks and review examples for topology leakage.

The execution VM must not receive Proxmox credentials, a hypervisor SSH private key, host filesystem mounts, or a Docker socket. Runner writable roots must remain narrow.