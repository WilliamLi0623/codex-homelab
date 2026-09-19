[CmdletBinding()]
param(
  [int]$FromStage=0,
  [int]$ToStage=19,
  [switch]$AllowDestruction
)
$ErrorActionPreference="Stop"
$root=Split-Path -Parent $MyInvocation.MyCommand.Path
$stageDir=Join-Path $root "stages"
$stages=@(
  @{N=0; F="00-preflight.ps1"},
  @{N=10;F="10-snapshot.ps1"},
  @{N=15;F="15-preserve-git.ps1"},
  @{N=18;F="18-independence-test.ps1"},
  @{N=19;F="19-readiness-gate.ps1"},
  @{N=20;F="20-destroy.ps1"},
  @{N=30;F="30-create-control.ps1"},
  @{N=40;F="40-create-runner.ps1"},
  @{N=50;F="50-configure-control.ps1"},
  @{N=60;F="60-configure-runner.ps1"},
  @{N=70;F="70-connect-webcodex.ps1"},
  @{N=75;F="75-restrict-windows-runner.ps1"},
  @{N=80;F="80-verify.ps1"}
)
foreach($s in $stages){
  if($s.N -lt $FromStage -or $s.N -gt $ToStage){continue}
  if($s.N -eq 20 -and !$AllowDestruction){throw "Stage $($s.N) requires -AllowDestruction"}
  & (Join-Path $stageDir $s.F)
}
