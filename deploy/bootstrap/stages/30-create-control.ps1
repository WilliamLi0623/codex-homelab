. "$PSScriptRoot\..\Common.ps1"
$stage="30-create-control"
Write-StageLog $stage "start"
$s=Get-State
if($s.destroy -ne "success"){throw "destroy stage not successful"}
$cfg=Get-SimpleConfig
$template=$cfg["control_template"]
$cmd="pct create 210 $template --hostname codex-control --cores 4 --memory 8192 --swap 1024 --rootfs $($cfg['storage']):32 --unprivileged 1 --features nesting=1 --net0 name=eth0,bridge=$($cfg['bridge']),ip=$($cfg['control_ip_cidr']),gw=$($cfg['gateway']) --nameserver $($cfg['nameserver']) --onboot 1 --startup order=20"
Invoke-Proxmox $cmd | Out-Null
Invoke-Proxmox "pct start 210" | Out-Null
Start-Sleep -Seconds 5
[void](Assert-Guest @{Id=210;Type="lxc";Name="codex-control"})
Set-Stage "control_created" "success"
Write-StageLog $stage "success"