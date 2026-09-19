[CmdletBinding()]
param(
  [switch]$Apply,
  [string]$ProxmoxHost = "100.64.2.121",
  [int]$TemplateVMID = 3005,
  [string]$RepoRoot = (Resolve-Path (Join-Path $PSScriptRoot "../..")).Path,
  [string]$GoExe = "go"
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"
$artifact = Join-Path ([IO.Path]::GetTempPath()) "codex-agentd-linux-amd64"
$remoteArtifact = "/tmp/codex-agentd-v3"

Write-Host "Worker runtime plan"
Write-Host "  template: $TemplateVMID@$ProxmoxHost"
Write-Host "  artifact: $artifact"
Write-Host "  action:   $(if ($Apply) { 'build and push agentd' } else { 'dry-run only' })"
Write-Host "  gate:     Codex CLI/runtime must be installed separately before E2E"

if (-not $Apply) { exit 0 }

Push-Location $RepoRoot
try {
  $env:GOOS = "linux"
  $env:GOARCH = "amd64"
  $env:CGO_ENABLED = "0"
  & $GoExe build -trimpath -ldflags "-s -w" -o $artifact ./cmd/agentd
  if ($LASTEXITCODE -ne 0) { throw "agentd build failed" }
} finally { Pop-Location }

try {
  & scp $artifact "root@${ProxmoxHost}:$remoteArtifact"
  if ($LASTEXITCODE -ne 0) { throw "copy to Proxmox failed" }
  & ssh "root@$ProxmoxHost" "set -e; pct push $TemplateVMID $remoteArtifact /usr/local/bin/codex-agentd --perms 0755; pct mount $TemplateVMID >/dev/null; test -x /var/lib/lxc/$TemplateVMID/rootfs/usr/local/bin/codex-agentd; pct unmount $TemplateVMID; rm -f $remoteArtifact"
  if ($LASTEXITCODE -ne 0) { throw "template staging or verification failed" }
} finally {
  Remove-Item -LiteralPath $artifact -Force -ErrorAction SilentlyContinue
}
