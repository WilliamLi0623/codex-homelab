param([string]$BootstrapRoot)
$ErrorActionPreference="Stop"
. (Join-Path $BootstrapRoot "Common.ps1")
$resultPath=Join-Path $BootstrapRoot "independence-result.json"
$r=[ordered]@{started=(Get-Date).ToUniversalTime().ToString("o"); stop_ok=$false; ssh_ok=$false; inventory_ok=$false; restart_ok=$false; completed=$null; error=$null}
try {
  $cfg=Get-SimpleConfig
  & ssh.exe $cfg["proxmox_alias"] "pct exec 210 -- systemctl stop webcodex.service"
  if($LASTEXITCODE -ne 0){throw "failed to stop webcodex.service"}
  $r.stop_ok=$true
  Start-Sleep -Seconds 3
  $o=& ssh.exe $cfg["proxmox_alias"] "qm list; pct list; pvesm status; ip -brief link show $($cfg['bridge'])" 2>&1
  if($LASTEXITCODE -ne 0){throw "standalone ssh/read-only inventory failed"}
  $r.ssh_ok=$true
  if(($o -join "`n") -match "codex-worker-01" -and ($o -join "`n") -match "webcodex-server"){$r.inventory_ok=$true}else{throw "inventory content incomplete"}
} catch {
  $r.error=$_.Exception.Message
} finally {
  try {
    $cfg=Get-SimpleConfig
    & ssh.exe $cfg["proxmox_alias"] "pct exec 210 -- systemctl start webcodex.service"
    if($LASTEXITCODE -eq 0){$r.restart_ok=$true}
  } catch {}
  $r.completed=(Get-Date).ToUniversalTime().ToString("o")
  $tmp="$resultPath.tmp.$PID"
  $r | ConvertTo-Json | Set-Content -Encoding UTF8 $tmp
  Move-Item -Force $tmp $resultPath
}
if(!($r.stop_ok -and $r.ssh_ok -and $r.inventory_ok -and $r.restart_ok)){exit 1}