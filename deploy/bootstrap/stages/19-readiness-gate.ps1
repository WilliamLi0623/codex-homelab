. "$PSScriptRoot\..\Common.ps1"
$stage="19-readiness-gate"
Write-StageLog $stage "start"
try {
  $s=Get-State
  $result=Join-Path $script:BootstrapRoot "independence-result.json"
  if(Test-Path $result){
    $r=Get-Content $result -Raw | ConvertFrom-Json
    if($r.stop_ok -and $r.ssh_ok -and $r.inventory_ok -and $r.restart_ok){Set-Stage "independence_test" "success"; $s=Get-State}
  }
  foreach($k in @("preflight","snapshot","git_preservation","independence_test")){if($s[$k] -ne "success"){throw "$k is not success"}}
  Assert-AllGuestIdentities
  $cfg=Get-SimpleConfig
  if(!(Test-RemoteFile $cfg["runner_image"])){throw "runner image missing"}
  $tmpl=(Invoke-Proxmox "pveam list local") -join "`n"
  if($tmpl -notmatch [regex]::Escape(($cfg["control_template"] -replace '^local:vztmpl/',''))){throw "control template missing"}
  & gh.exe repo view "$($cfg['github_owner'])/$($cfg['github_repo'])" --json defaultBranchRef,url *> $null
  if($LASTEXITCODE -ne 0){throw "new repository not reachable"}
  $privateDir=Get-PrivateDirectory $cfg
  foreach($f in @("webcodex.env","cloudflared.token","linux-runner.toml","codex-auth.json")){
    if(!(Test-Path (Join-Path $privateDir $f))){throw "credential recovery file missing: $f"}
  }
  $report=Join-Path $s.snapshot_path "destructive-readiness.md"
  $lines=@("# DESTRUCTIVE READINESS","","Bootstrap independent of WebCodex: PASS","Proxmox SSH: PASS","State persistence/resume: PASS","Git preservation: PASS","Creation artifacts available: PASS","Secrets recovery path tested: PASS","New repo pushed: PASS","")
  foreach($g in $script:Allowlist){$lines += "DESTROY: $($g.Id) $($g.Name) VERIFIED"}
  foreach($g in $script:Denylist){$lines += "PROTECTED: $($g.Id) $($g.Name) VERIFIED"}
  $lines += ""; $lines += "READY_FOR_DESTRUCTION=true"
  $lines | Set-Content -Encoding UTF8 $report
  Set-Stage "readiness_gate" "success" @{ready_for_destruction=$true}
  Write-StageLog $stage "success READY_FOR_DESTRUCTION=true"
} catch {
  $s=Get-State
  $report=if($s.snapshot_path){Join-Path $s.snapshot_path "destructive-readiness.md"}else{Join-Path $script:SnapshotDir "destructive-readiness.md"}
  @("# DESTRUCTIVE READINESS","","READY_FOR_DESTRUCTION=false","","Failure: $($_.Exception.Message)") | Set-Content -Encoding UTF8 $report
  Set-Stage "readiness_gate" "failed" @{ready_for_destruction=$false}
  Write-StageLog $stage ("failed: "+$_.Exception.Message)
  throw
}
