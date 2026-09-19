param([string]$BootstrapRoot)
$ErrorActionPreference="Stop"
$resultPath=Join-Path $BootstrapRoot "independence-result.json"
$childLog=Join-Path $BootstrapRoot "logs\18-independence-child.log"
Add-Content -Path $childLog -Value ((Get-Date).ToUniversalTime().ToString("o")+" detached child start pid="+$PID)
. (Join-Path $BootstrapRoot "Common.ps1")
$r=[ordered]@{started=(Get-Date).ToUniversalTime().ToString("o"); stop_ok=$false; ssh_ok=$false; inventory_ok=$false; restart_ok=$false; completed=$null; error=$null}
try {
  $cfg=Get-SimpleConfig
  & ssh.exe -o BatchMode=yes -o ConnectTimeout=10 $cfg["proxmox_alias"] "pct exec 210 -- systemctl stop cloudflared.service webcodex.socket webcodex.service"
  if($LASTEXITCODE -ne 0){throw "failed to stop old WebCodex control path"}
  Start-Sleep -Seconds 3
  $down=& ssh.exe -o BatchMode=yes -o ConnectTimeout=10 $cfg["proxmox_alias"] "pct exec 210 -- bash -lc 'systemctl is-active webcodex.service webcodex.socket cloudflared.service || true'" 2>&1
  if(($down -join " ") -match "\bactive\b"){throw "old WebCodex control path still active"}
  $r.stop_ok=$true
  $o=& ssh.exe -o BatchMode=yes -o ConnectTimeout=10 $cfg["proxmox_alias"] "qm list; pct list; pvesm status; ip -brief link show $($cfg['bridge'])" 2>&1
  if($LASTEXITCODE -ne 0){throw "standalone ssh/read-only inventory failed"}
  $r.ssh_ok=$true
  if(($o -join "`n") -match "codex-worker-01" -and ($o -join "`n") -match "webcodex-server"){$r.inventory_ok=$true}else{throw "inventory content incomplete"}
} catch {
  $r.error=$_.Exception.Message
} finally {
  try {
    $cfg=Get-SimpleConfig
    & ssh.exe -o BatchMode=yes -o ConnectTimeout=10 $cfg["proxmox_alias"] "pct exec 210 -- systemctl start webcodex.socket webcodex.service cloudflared.service"
    if($LASTEXITCODE -eq 0){
      Start-Sleep -Seconds 3
      $up=& ssh.exe -o BatchMode=yes -o ConnectTimeout=10 $cfg["proxmox_alias"] "pct exec 210 -- bash -lc 'systemctl is-active webcodex.service cloudflared.service'" 2>&1
      if(($up -join " ") -match "active"){$r.restart_ok=$true}
    }
  } catch {}
  $r.completed=(Get-Date).ToUniversalTime().ToString("o")
  $tmp="$resultPath.tmp.$PID"
  $r | ConvertTo-Json | Set-Content -Encoding UTF8 $tmp
  Move-Item -Force $tmp $resultPath
}
Add-Content -Path $childLog -Value ((Get-Date).ToUniversalTime().ToString("o")+" detached child complete stop_ok="+$r.stop_ok+" ssh_ok="+$r.ssh_ok+" inventory_ok="+$r.inventory_ok+" restart_ok="+$r.restart_ok+" error="+$r.error)
if(!($r.stop_ok -and $r.ssh_ok -and $r.inventory_ok -and $r.restart_ok)){exit 1}