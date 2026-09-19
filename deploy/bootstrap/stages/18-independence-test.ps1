. "$PSScriptRoot\..\Common.ps1"
$stage="18-independence-test"
Write-StageLog $stage "start"
$s=Get-State
if($s.git_preservation -ne "success"){throw "git preservation not successful"}
$result=Join-Path $script:BootstrapRoot "independence-result.json"
if(Test-Path $result){
  $r=Get-Content $result -Raw | ConvertFrom-Json
  if($r.stop_ok -and $r.ssh_ok -and $r.inventory_ok -and $r.restart_ok){
    Set-Stage "independence_test" "success"
    Write-StageLog $stage "verified previous child result"
    return
  }
}
Remove-Item -Force -ErrorAction SilentlyContinue $result
$child=Join-Path $PSScriptRoot "18-independence-child.ps1"
$p=Start-Process powershell.exe -ArgumentList @("-NoProfile","-ExecutionPolicy","Bypass","-File",$child,"-BootstrapRoot",$script:BootstrapRoot) -PassThru
Write-StageLog $stage ("child_started pid="+$p.Id)
Write-Output "Independence child launched PID=$($p.Id). Re-run stage 18 after WebCodex reconnects to verify the result."