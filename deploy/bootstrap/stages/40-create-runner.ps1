. "$PSScriptRoot\..\Common.ps1"
$stage="40-create-runner"
Write-StageLog $stage "start"
$s=Get-State
if($s.control_created -ne "success"){throw "control not created"}
$cfg=Get-SimpleConfig
$remotePub="/root/codex-homelab-runner.pub"
New-RemotePublicKeyFile $cfg $remotePub | Out-Null
try {
  Invoke-Proxmox "qm create 101 --name codex-runner-01 --cores 8 --memory 16384 --cpu host --machine q35 --net0 virtio,bridge=$($cfg['bridge']) --scsihw virtio-scsi-single --agent enabled=1 --onboot 1" | Out-Null
  Invoke-Proxmox "qm importdisk 101 $($cfg['runner_image']) $($cfg['storage'])" | Out-Null
  $conf=(Invoke-Proxmox "qm config 101") -join "`n"
  $m=[regex]::Match($conf,'(?m)^unused0:\s*([^,\r\n]+)')
  if(!$m.Success){throw "imported disk not found as unused0"}
  $disk=$m.Groups[1].Value.Trim()
  Invoke-Proxmox "qm set 101 --scsi0 $disk,discard=on,iothread=1,ssd=1 --ide2 $($cfg['storage']):cloudinit --boot order=scsi0 --serial0 socket --vga serial0 --ciuser webcodex --sshkeys $remotePub --ipconfig0 ip=$($cfg['runner_ip_cidr']),gw=$($cfg['gateway']) --nameserver $($cfg['nameserver'])" | Out-Null
  Invoke-Proxmox "qm resize 101 scsi0 100G" | Out-Null
  Invoke-Proxmox "qm start 101" | Out-Null
  Set-Stage "runner_created" "success"
  Write-StageLog $stage "success"
} finally {
  Remove-RemotePublicKeyFile $cfg $remotePub
}
