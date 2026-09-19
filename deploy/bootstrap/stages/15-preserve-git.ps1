. "$PSScriptRoot\..\Common.ps1"
$stage="15-preserve-git"
Write-StageLog $stage "start"
try {
  $s=Get-State
  if($s.snapshot -ne "success"){throw "snapshot not successful"}
  $report=Join-Path $s.snapshot_path "git-preservation-report.md"
  if(!(Test-Path $report)){throw "missing git-preservation-report.md"}
  $text=Get-Content $report -Raw
  if($text -match "(?i)\bUNCLASSIFIED\b"){throw "git preservation report contains UNCLASSIFIED"}
  if($text -notmatch "(?i)PRESERVED|DISCARDED_INTENTIONALLY"){throw "git preservation report has no classifications"}
  Set-Stage "git_preservation" "success"
  Write-StageLog $stage "success"
} catch {
  Set-Stage "git_preservation" "failed" @{ready_for_destruction=$false}
  Write-StageLog $stage ("failed: "+$_.Exception.Message)
  throw
}