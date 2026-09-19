. "$PSScriptRoot\..\Common.ps1"
$stage="50-configure-control"
Write-StageLog $stage "start"
$s=Get-State
if($s.runner_created -ne "success"){throw "runner not created"}
$cfg=Get-SimpleConfig
$r=$cfg["recovery_dir"]
Invoke-Proxmox "pct exec 210 -- bash -lc 'apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y ca-certificates curl git jq sqlite3 xz-utils'" | Out-Null
Invoke-Proxmox "pct exec 210 -- mkdir -p /etc/webcodex /etc/cloudflared /var/lib/webcodex /var/lib/codex-controller" | Out-Null
Invoke-Proxmox "pct push 210 $r/node-v20.19.2-linux-x64.tar.xz /tmp/node.tar.xz --perms 600" | Out-Null
Invoke-Proxmox "pct exec 210 -- bash -lc 'tar -C /usr/local --strip-components=1 -xJf /tmp/node.tar.xz; rm -f /tmp/node.tar.xz'" | Out-Null
foreach($b in @("webcodex","webcodex-server")){ Invoke-Proxmox "pct push 210 $r/$b-0.4.1 /usr/local/bin/$b --perms 755" | Out-Null }
Invoke-Proxmox "pct push 210 $r/webcodex.env /etc/webcodex/webcodex.env --perms 600" | Out-Null
Invoke-Proxmox "pct push 210 $r/cloudflared.token /etc/cloudflared/token --perms 600" | Out-Null
Invoke-Proxmox "pct push 210 $r/cloudflared-2026.9.1 /usr/bin/cloudflared --perms 755" | Out-Null
$webUnit=@"
[Unit]
Description=WebCodex Runtime
After=network-online.target
Wants=network-online.target
[Service]
Type=simple
EnvironmentFile=/etc/webcodex/webcodex.env
ExecStart=/usr/local/bin/webcodex-server
Restart=on-failure
RestartSec=3
WorkingDirectory=/var/lib/webcodex
[Install]
WantedBy=multi-user.target
"@
$cfUnit=@"
[Unit]
Description=Cloudflare Tunnel client
After=network-online.target
Wants=network-online.target
[Service]
Type=notify
ExecStart=/usr/bin/cloudflared --no-autoupdate tunnel run --token-file /etc/cloudflared/token
Restart=on-failure
RestartSec=5s
[Install]
WantedBy=multi-user.target
"@
$tmp1="/root/webcodex-v2.service"; $tmp2="/root/cloudflared-v2.service"
$webUnit | & ssh.exe $cfg["proxmox_alias"] "cat > $tmp1"
$cfUnit | & ssh.exe $cfg["proxmox_alias"] "cat > $tmp2"
Invoke-Proxmox "pct push 210 $tmp1 /etc/systemd/system/webcodex.service --perms 644; pct push 210 $tmp2 /etc/systemd/system/cloudflared.service --perms 644; rm -f $tmp1 $tmp2" | Out-Null
Invoke-Proxmox "pct exec 210 -- bash -lc 'systemctl daemon-reload; systemctl enable --now webcodex.service cloudflared.service'" | Out-Null
Start-Sleep -Seconds 3
$active=(Invoke-Proxmox "pct exec 210 -- systemctl is-active webcodex.service") -join ""
if($active.Trim() -ne "active"){throw "WebCodex server failed to become active"}
Set-Stage "control_configured" "success"
Write-StageLog $stage "success"