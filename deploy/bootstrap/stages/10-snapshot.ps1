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
  $meta=@()
  $meta += "webcodex="+(((Invoke-Proxmox "pct exec 210 -- /usr/local/bin/webcodex -V" -AllowFailure) -join " ").Trim())
  $meta += "node="+(((Invoke-Proxmox "pct exec 210 -- node --version" -AllowFailure) -join " ").Trim())
  $meta += "npm="+(((Invoke-Proxmox "pct exec 210 -- npm --version" -AllowFailure) -join " ").Trim())
  $meta += "cloudflared="+(((Invoke-Proxmox "pct exec 210 -- cloudflared --version" -AllowFailure) -join " ").Trim())
  $meta | Set-Content -Encoding UTF8 (Join-Path $dest "versions.txt")
  (Invoke-Proxmox "pct exec 210 -- systemctl cat webcodex.service webcodex.socket cloudflared.service" -AllowFailure) | Set-Content -Encoding UTF8 (Join-Path $dest "control-units.txt")
  (Invoke-Proxmox "stat -c '%a %U %G %n' /root/codex-homelab-recovery; find /root/codex-homelab-recovery -maxdepth 1 -type f -printf '%m %U %G %f %s bytes\n' | sort") | Set-Content -Encoding UTF8 (Join-Path $dest "recovery-artifacts-metadata.txt")
  Set-Stage "snapshot" "success" @{snapshot_path=$dest}
  Write-StageLog $stage "success snapshot=$dest"
} catch {
  Set-Stage "snapshot" "failed" @{ready_for_destruction=$false}
  Write-StageLog $stage ("failed: "+$_.Exception.Message)
  throw
}