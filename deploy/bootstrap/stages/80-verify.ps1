. "$PSScriptRoot\..\Common.ps1"
$stage="80-verify"
Write-StageLog $stage "start"
try {
  [void](Assert-Guest @{Id=210;Type="lxc";Name="codex-control"})
  [void](Assert-Guest @{Id=101;Type="vm";Name="codex-runner-01"})
  foreach($g in $script:Denylist){[void](Assert-Guest $g)}
  $server=((Invoke-Proxmox "pct exec 210 -- systemctl is-active webcodex.service") -join "").Trim()
  if($server -ne "active"){throw "WebCodex server not active"}
  $cfg=Get-SimpleConfig
  $ip=($cfg["runner_ip_cidr"] -split "/")[0]
  $runner=& ssh.exe -o StrictHostKeyChecking=accept-new "webcodex@$ip" "systemctl is-active webcodex-runner.service; test -w /srv/codex && echo workspace-writable; codex --version; webcodex --version"
  if($LASTEXITCODE -ne 0 -or ($runner -join "`n") -notmatch "(?m)^active$"){throw "Linux runner unhealthy"}
  $s=Get-State
  if($s.windows_runner_restricted -ne "success"){throw "Windows runner restriction not completed"}
  Set-Stage "verified" "success"
  Write-StageLog $stage "basic infrastructure verification success"
} catch {
  Set-Stage "verified" "failed"
  Write-StageLog $stage ("failed: "+$_.Exception.Message)
  throw
}