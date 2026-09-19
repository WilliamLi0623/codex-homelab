# Deployment

1. Prepare one Proxmox host and Windows bootstrap host.
2. Copy config.example.yaml to the private runtime config.yaml and fill local values.
3. Run rebuild.ps1 through stage 19.
4. Inspect the generated destructive-readiness report.
5. Only when readiness is computed true may rebuild.ps1 be launched from stage 20 with -AllowDestruction in standalone Windows PowerShell.