. "$PSScriptRoot\..\Common.ps1"
$stage="20-destroy"
Write-StageLog $stage "start"
$s=Get-State
if($s.readiness_gate -ne "success" -or !$s.ready_for_destruction){throw "READY_FOR_DESTRUCTION is not true"}
$order=@(101,102,104,105,106,107,108,109,9701,103,210)
foreach($id in $order){
  $g=$script:Allowlist | Where-Object {$_.Id -eq $id} | Select-Object -First 1
  if(!$g){throw "ID $id is not allowlisted"}
  [void](Assert-Guest $g)
  Write-StageLog $stage ("verified $($g.Type) $($g.Id) $($g.Name)")
  if($g.Type -eq "vm"){
    Invoke-Proxmox "qm stop $id" -AllowFailure | Out-Null
    Start-Sleep -Seconds 2
    $status=(Invoke-Proxmox "qm status $id") -join " "
    if($status -notmatch "stopped"){throw "VM $id did not stop"}
    Invoke-Proxmox "qm set $id --protection 0" -AllowFailure | Out-Null
    Invoke-Proxmox "qm destroy $id --purge 1 --destroy-unreferenced-disks 1" | Out-Null
    $probe=Invoke-Proxmox "qm config $id >/dev/null 2>&1; echo `$?" -AllowFailure
    if(($probe -join "").Trim() -eq "0"){throw "VM $id still exists after destroy"}
  } else {
    Invoke-Proxmox "pct stop $id" -AllowFailure | Out-Null
    Start-Sleep -Seconds 2
    Invoke-Proxmox "pct destroy $id --purge 1 --destroy-unreferenced-disks 1" | Out-Null
    $probe=Invoke-Proxmox "pct config $id >/dev/null 2>&1; echo `$?" -AllowFailure
    if(($probe -join "").Trim() -eq "0"){throw "LXC $id still exists after destroy"}
  }
  Write-StageLog $stage ("destroyed $($g.Type) $id")
}
foreach($g in $script:Denylist){[void](Assert-Guest $g)}
Set-Stage "destroy" "success"
Write-StageLog $stage "success"