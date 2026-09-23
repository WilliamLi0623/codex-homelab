[CmdletBinding()]
param(
  [Parameter(Mandatory = $true)] [string]$ProxmoxAlias,
  [Parameter(Mandatory = $true)] [ValidateScript({ Test-Path -LiteralPath $_ -PathType Leaf })] [string]$PublicKeyFile,
  [int[]]$LxcId = @(),
  [int[]]$VmId = @(),
  [string]$VmUser = "webcodex",
  [string]$BackupRoot,
  [switch]$StartStopped,
  [switch]$WhatIf
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

function Get-PublicKeyLine([string]$Path) {
  $lines = @(Get-Content -LiteralPath $Path | ForEach-Object { $_.Trim() } | Where-Object { $_ })
  if ($lines.Count -ne 1) { throw "public key file must contain exactly one non-empty line" }
  $line = $lines[0]
  if ($line -notmatch '^(ssh-ed25519|ssh-rsa|ecdsa-sha2-nistp256|ecdsa-sha2-nistp384|ecdsa-sha2-nistp521|sk-ssh-ed25519|sk-ecdsa-sha2-nistp256)\s+[A-Za-z0-9+/=]+(?:\s+.*)?$') {
    throw "public key is not a supported OpenSSH public-key line"
  }
  return $line
}

function ConvertTo-Base64String([string]$Text) {
  return [Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes($Text))
}

function Invoke-Pve([string]$Command, [switch]$AllowFailure) {
  $out = & ssh.exe -o BatchMode=yes -o ConnectTimeout=10 $ProxmoxAlias $Command 2>&1
  $code = $LASTEXITCODE
  if ($code -ne 0 -and !$AllowFailure) { throw "PVE command failed ($code): $Command :: $($out -join ' ')" }
  return ,$out
}

function Invoke-PveGuestScript([int]$Id, [string]$Script) {
  $out = $Script | & ssh.exe -o BatchMode=yes -o ConnectTimeout=10 $ProxmoxAlias "qm guest exec $Id --synchronous 1 --pass-stdin 1 -- bash -s" 2>&1
  $code = $LASTEXITCODE
  if ($code -ne 0) { throw "QEMU Guest Agent command failed for VM $Id ($code): $($out -join ' ')" }
  return ,$out
}

function Assert-GuestId([int]$Id) {
  if ($Id -lt 100 -or $Id -gt 999999999) { throw "invalid guest ID: $Id" }
}

function Get-LxcStatus([int]$Id) {
  $text = (Invoke-Pve "pct status $Id") -join "`n"
  $m = [regex]::Match($text, '(?m)^status:\s*(running|stopped)\s*$')
  if (!$m.Success) { throw "unable to determine LXC $Id status" }
  return $m.Groups[1].Value
}

function Test-LxcTemplate([int]$Id) {
  $text = (Invoke-Pve "pct config $Id") -join "`n"
  return $text -match '(?m)^template:\s*1\s*$'
}

function Backup-LxcConfig([int]$Id) {
  Invoke-Pve "cp -a /etc/pve/lxc/$Id.conf '$BackupRoot/lxc-$Id.conf'" | Out-Null
}

function Backup-VmConfig([int]$Id) {
  $config = (Invoke-Pve "qm config $Id") -join "`n"
  $encoded = ConvertTo-Base64String $config
  Invoke-Pve "printf %s '$encoded' | base64 -d > '$BackupRoot/vm-$Id.conf'; chmod 600 '$BackupRoot/vm-$Id.conf'" | Out-Null
}

function Invoke-LxcInjection([int]$Id, [string]$Stamp) {
  Assert-GuestId $Id
  $status = Get-LxcStatus $Id
  if ($WhatIf) {
    Write-Output "WHATIF LXC $Id status=$status target=/root/.ssh/authorized_keys"
    return
  }
  Backup-LxcConfig $Id
  $startedByUs = $false
  $templateCleared = $false
  $keyPath = "/root/.ssh/.codex-homelab-key-$Stamp"
  try {
    if ($status -eq "stopped") {
      if (!$StartStopped) { throw "LXC $Id is stopped; pass -StartStopped to modify it" }
      if (Test-LxcTemplate $Id) {
        Invoke-Pve "pct set $Id --template 0" | Out-Null
        $templateCleared = $true
      }
      Invoke-Pve "pct start $Id" | Out-Null
      $startedByUs = $true
      Start-Sleep -Seconds 3
    }
    Invoke-Pve "pct push $Id '$script:RemotePublicKeyPath' '$keyPath'" | Out-Null
    $remote = @'
set -eu
auth=/root/.ssh/authorized_keys
key=__KEY_PATH__
backup=/root/.ssh/authorized_keys.pre-codex-key-__STAMP__
missing=/root/.ssh/authorized_keys.missing-pre-codex-key-__STAMP__
trap 'rm -f "$key"' EXIT
install -d -m 700 /root/.ssh
if [ -e "$auth" ]; then cp -p "$auth" "$backup"; else : > "$missing"; fi
touch "$auth"
key_value=$(tr -d '\r\n' < "$key")
if ! grep -Fqx "$key_value" "$auth"; then printf '%s\n' "$key_value" >> "$auth"; fi
chmod 600 "$auth"
count=$(grep -Fxc "$key_value" "$auth")
key_hash=$(printf '%s' "$key_value" | sha256sum | awk '{print $1}')
mode=$(stat -c '%a' "$auth")
test "$count" -eq 1
printf 'KEY_HASH=%s COUNT=%s MODE=%s\n' "$key_hash" "$count" "$mode"
'@
    $remote = $remote.Replace("__KEY_PATH__", $keyPath).Replace("__STAMP__", $Stamp)
    $encoded = ConvertTo-Base64String $remote
    $out = Invoke-Pve "pct exec $Id -- sh -c 'printf %s $encoded | base64 -d | sh'"
    $text = $out -join "`n"
    if ($text -notmatch 'KEY_HASH=[0-9a-f]{64} COUNT=1 MODE=600') { throw "LXC $Id verification failed: $text" }
    Write-Output (($text | Select-String -Pattern 'KEY_HASH=.*').ToString().Trim())
  } finally {
    if ($startedByUs) { Invoke-Pve "pct stop $Id" -AllowFailure | Out-Null }
    if ($templateCleared) { Invoke-Pve "pct set $Id --template 1" -AllowFailure | Out-Null }
  }
}

function Invoke-VmInjection([int]$Id, [string]$Stamp) {
  Assert-GuestId $Id
  if ($WhatIf) {
    Write-Output "WHATIF VM $Id user=$VmUser target=~/.ssh/authorized_keys"
    return
  }
  Backup-VmConfig $Id
  $keyB64 = ConvertTo-Base64String $script:PublicKey
  $guest = @'
set -eu
user=__USER__
key_value=$(printf '%s' '__KEY_B64__' | base64 -d)
home=$(getent passwd "$user" | cut -d: -f6)
test -n "$home"
auth="$home/.ssh/authorized_keys"
backup="$home/.ssh/authorized_keys.pre-codex-key-__STAMP__"
missing="$home/.ssh/authorized_keys.missing-pre-codex-key-__STAMP__"
install -d -m 700 -o "$user" -g "$user" "$home/.ssh"
if [ -e "$auth" ]; then cp -p "$auth" "$backup"; else : > "$missing"; fi
touch "$auth"
if ! grep -Fqx "$key_value" "$auth"; then printf '%s\n' "$key_value" >> "$auth"; fi
chown "$user":"$user" "$auth"
chmod 600 "$auth"
count=$(grep -Fxc "$key_value" "$auth")
key_hash=$(printf '%s' "$key_value" | sha256sum | awk '{print $1}')
mode=$(stat -c '%a' "$auth")
test "$count" -eq 1
printf 'KEY_HASH=%s COUNT=%s MODE=%s USER=%s\n' "$key_hash" "$count" "$mode" "$user"
'@
  $guest = $guest.Replace("__USER__", $VmUser).Replace("__KEY_B64__", $keyB64).Replace("__STAMP__", $Stamp)
  $out = Invoke-PveGuestScript $Id $guest
  $text = $out -join "`n"
  if ($text -notmatch 'KEY_HASH=[0-9a-f]{64} COUNT=1 MODE=600 USER=') { throw "VM $Id verification failed: $text" }
  Write-Output (($text | Select-String -Pattern 'KEY_HASH=.*').ToString().Trim())
}

$script:PublicKey = Get-PublicKeyLine $PublicKeyFile
$sha = [Security.Cryptography.SHA256]::Create()
$expectedHash = ([BitConverter]::ToString($sha.ComputeHash([Text.Encoding]::UTF8.GetBytes($script:PublicKey))) -replace '-', '').ToLowerInvariant()
$sha.Dispose()
$stamp = (Get-Date).ToUniversalTime().ToString('yyyyMMddTHHmmssZ') + "-$PID"
if (!$BackupRoot) { $BackupRoot = "/root/codex-ssh-key-backups-$stamp" }
if ($BackupRoot -notmatch '^/root/[A-Za-z0-9._/-]+$') { throw "unsafe backup root: $BackupRoot" }
$script:RemotePublicKeyPath = "/root/codex-homelab-key-$stamp.pub"

if ($LxcId.Count -eq 0 -and $VmId.Count -eq 0) { throw "specify at least one LXC or VM ID" }
foreach ($id in $LxcId) { Assert-GuestId $id }
foreach ($id in $VmId) { Assert-GuestId $id }

if ($WhatIf) {
  Write-Output "WHATIF key_sha256=$expectedHash backup_root=$BackupRoot"
  foreach ($id in $LxcId) { Invoke-LxcInjection $id $stamp }
  foreach ($id in $VmId) { Invoke-VmInjection $id $stamp }
  exit 0
}

Invoke-Pve "install -d -m 700 '$BackupRoot'" | Out-Null
$script:PublicKey | & ssh.exe -o BatchMode=yes -o ConnectTimeout=10 $ProxmoxAlias "umask 077; cat > '$script:RemotePublicKeyPath'; chown root:root '$script:RemotePublicKeyPath'; chmod 600 '$script:RemotePublicKeyPath'; test -s '$script:RemotePublicKeyPath'" 2>&1 | Out-Null
if ($LASTEXITCODE -ne 0) { throw "failed to upload temporary public key to PVE" }
try {
  foreach ($id in $LxcId) { Invoke-LxcInjection $id $stamp }
  foreach ($id in $VmId) { Invoke-VmInjection $id $stamp }
  Write-Output "verified_key_sha256=$expectedHash backup_root=$BackupRoot"
} finally {
  Invoke-Pve "rm -f -- '$script:RemotePublicKeyPath'" -AllowFailure | Out-Null
}
