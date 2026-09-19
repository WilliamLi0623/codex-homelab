. "$PSScriptRoot\..\Common.ps1"
$stage="10-snapshot"
Write-StageLog $stage "start"
try {
  $s=Get-State
  if($s.preflight -ne "success"){throw "preflight not successful"}
  $stamp=(Get-Date).ToUniversalTime().ToString("yyyyMMddTHHmmssZ")
  $dest=Join-Path $script:SnapshotDir $stamp
  New-Item -ItemType Directory -Force -Path $dest | Out-Null
  $cmds=@{
    "qm-list.txt"="qm list"; "pct-list.txt"="pct list"; "pvesm-status.txt"="pvesm status";
    "network.txt"="ip -brief addr; ip -brief link; ip route"; "host-root.txt"="df -h /; pveversion -v"
  }
  foreach($name in $cmds.Keys){ (Invoke-Proxmox $cmds[$name]) | Set-Content -Encoding UTF8 (Join-Path $dest $name) }
  foreach($g in $script:Allowlist){
    $cmd=if($g.Type -eq "vm"){"qm config $($g.Id)"}else{"pct config $($g.Id)"}
    (Invoke-Proxmox $cmd) | Set-Content -Encoding UTF8 (Join-Path $dest "$($g.Type)-$($g.Id)-config.txt")
  }
  (Invoke-Proxmox "pct exec 210 -- bash -lc ""webcodex --version 2>/dev/null || true; node --version 2>/dev/null || true; npm --version 2>/dev/null || true; cloudflared --version 2>/dev/null || true; systemctl cat webcodex.service webcodex.socket 2>/dev/null || true; echo ENV_KEYS; sed -E 's/=.*$/=<redacted>/' /etc/webcodex/webcodex.env 2>/dev/null""") | Set-Content -Encoding UTF8 (Join-Path $dest "webcodex-server-metadata.txt")
  Set-Stage "snapshot" "success" @{snapshot_path=$dest}
  Write-StageLog $stage "success snapshot=$dest"
} catch {
  Set-Stage "snapshot" "failed" @{ready_for_destruction=$false}
  Write-StageLog $stage ("failed: "+$_.Exception.Message)
  throw
}