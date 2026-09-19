. "$PSScriptRoot\..\Common.ps1"
$stage="00-preflight"
Write-StageLog $stage "start"
try {
  $cfg=Get-SimpleConfig
  foreach($k in @("proxmox_alias","storage","bridge","control_template","runner_image","control_ip_cidr","runner_ip_cidr","gateway","nameserver","ssh_public_key_file","recovery_dir","github_owner","github_repo")){
    if(!$cfg[$k]){throw "config missing $k"}
  }
  & ssh.exe $cfg["proxmox_alias"] "true" | Out-Null
  if($LASTEXITCODE -ne 0){throw "Proxmox SSH failed"}
  Assert-AllGuestIdentities
  $pvesm=(Invoke-Proxmox "pvesm status") -join "`n"
  if($pvesm -notmatch "(?m)^$([regex]::Escape($cfg['storage']))\s+\S+\s+active\s"){throw "required storage not active"}
  $bridge=(Invoke-Proxmox "ip -brief link show $($cfg['bridge'])") -join "`n"
  if($bridge -notmatch [regex]::Escape($cfg["bridge"])){throw "bridge missing"}
  if(!(Test-RemoteFile $cfg["runner_image"])){throw "runner image missing"}
  $tmpl=(Invoke-Proxmox "pveam list local") -join "`n"
  if($tmpl -notmatch [regex]::Escape(($cfg["control_template"] -replace '^local:vztmpl/',''))){throw "control template missing"}
  if(!(Test-Path $cfg["ssh_public_key_file"])){throw "SSH public key missing"}
  foreach($asset in @("webcodex.env","cloudflared.token","cloudflared-2026.9.1","webcodex-server-0.4.1","webcodex-runner-0.4.1","webcodex-0.4.1","codex-auth.json","node-v20.19.2-linux-x64.tar.xz","openai-codex-0.155.0.tgz","openai-codex-0.155.0-linux-x64.tgz")){
    if(!(Test-RemoteFile "$($cfg['recovery_dir'])/$asset")){throw "recovery asset missing: $asset"}
  }
  $drive=Get-PSDrive -Name C
  if($drive.Free -lt 2GB){throw "Windows disk free <2GB"}
  & gh.exe auth status *> $null
  if($LASTEXITCODE -ne 0){throw "GitHub auth failed"}
  Set-Stage "preflight" "success"
  Write-StageLog $stage "success"
} catch {
  Set-Stage "preflight" "failed" @{ready_for_destruction=$false}
  Write-StageLog $stage ("failed: "+$_.Exception.Message)
  throw
}