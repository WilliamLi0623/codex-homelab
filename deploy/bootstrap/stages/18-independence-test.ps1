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
$cmd='powershell.exe -NoProfile -ExecutionPolicy Bypass -File "'+$child+'" -BootstrapRoot "'+$script:BootstrapRoot+'"'
$created=Invoke-CimMethod -ClassName Win32_Process -MethodName Create -Arguments @{CommandLine=$cmd}
if($created.ReturnValue -ne 0){throw "Win32_Process.Create failed: $($created.ReturnValue)"}
Write-StageLog $stage ("detached_child_started pid="+$created.ProcessId)
Write-Output "Independence child launched detached via Win32_Process PID=$($created.ProcessId). Re-run stage 18 after WebCodex reconnects to verify the result."