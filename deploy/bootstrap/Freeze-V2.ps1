[CmdletBinding()]
param(
  [string]$RuntimeRoot = $PSScriptRoot
)

$ErrorActionPreference = "Stop"
$RuntimeRoot = (Resolve-Path -LiteralPath $RuntimeRoot).Path
$commonPath = Join-Path $RuntimeRoot "Common.ps1"
if (!(Test-Path -LiteralPath $commonPath)) {
  throw "Bootstrap Common.ps1 is missing: $commonPath"
}

. $commonPath

$timestamp = (Get-Date).ToUniversalTime().ToString("yyyyMMddTHHmmssZ")
$backupDir = Join-Path $script:SnapshotDir (Join-Path $timestamp "v3-freeze")
New-Item -ItemType Directory -Force -Path $backupDir | Out-Null
if (Test-Path -LiteralPath $script:StatePath) {
  Copy-Item -LiteralPath $script:StatePath -Destination (Join-Path $backupDir "state-before-v3-freeze.json") -Force
}

$state = Get-State
$state["PLAN_VERSION"] = "v3-final"
$state["OLD_PLAN_SUPERSEDED"] = $true
$state["v2_execution"] = "frozen"
$state["ready_for_destruction"] = $false
Save-StateAtomic $state

$markerPath = Join-Path $RuntimeRoot "v3-plan-freeze.json"
$marker = [ordered]@{
  PLAN_VERSION = "v3-final"
  OLD_PLAN_SUPERSEDED = $true
  frozen_at_utc = (Get-Date).ToUniversalTime().ToString("o")
  state_backup = (Join-Path $backupDir "state-before-v3-freeze.json")
}
$temporaryMarker = "$markerPath.tmp.$PID"
[IO.File]::WriteAllText($temporaryMarker, ($marker | ConvertTo-Json -Depth 4), (New-Object Text.UTF8Encoding($false)))
Move-Item -LiteralPath $temporaryMarker -Destination $markerPath -Force
Write-StageLog "v3-migration" "V2 bootstrap frozen; V3 migration state persisted"
