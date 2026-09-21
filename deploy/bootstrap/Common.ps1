Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

$script:BootstrapRoot = $PSScriptRoot
$script:ConfigPath = Join-Path $script:BootstrapRoot "config.yaml"
$script:StatePath  = Join-Path $script:BootstrapRoot "state.json"
$script:LogsDir    = Join-Path $script:BootstrapRoot "logs"
$script:SnapshotDir= Join-Path $script:BootstrapRoot "snapshot"
New-Item -ItemType Directory -Force -Path $script:LogsDir,$script:SnapshotDir | Out-Null

$script:Allowlist = @(
  @{Id=101; Type="vm";  Name="codex-runner-01"},
  @{Id=102; Type="vm";  Name="build-01"},
  @{Id=104; Type="vm";  Name="codex-worker-template"},
  @{Id=105; Type="vm";  Name="codex-worker-template-agent"},
  @{Id=106; Type="vm";  Name="codex-worker-template-bootstrap"},
  @{Id=107; Type="vm";  Name="codex-worker-template-explicit"},
  @{Id=108; Type="vm";  Name="codex-worker-template-ubuntu"},
  @{Id=109; Type="vm";  Name="codex-worker-template-ubuntu-static"},
  @{Id=9701;Type="vm";  Name="pilot-9701"},
  @{Id=103; Type="lxc"; Name="codex-controller"},
  @{Id=210; Type="lxc"; Name="codex-control"}
)
$script:Denylist = @(
  @{Id=100; Type="lxc"; Name="tailscale-alt"},
  @{Id=110; Type="lxc"; Name="gpt-oss-cpu-bench"},
  @{Id=200; Type="lxc"; Name="grafana-monitor"}
)

function Get-SimpleConfig {
  if (!(Test-Path $script:ConfigPath)) { throw "Missing config: $script:ConfigPath" }
  $h = @{}
  foreach ($line in Get-Content $script:ConfigPath) {
    $t=$line.Trim()
    if(!$t -or $t.StartsWith("#")) { continue }
    $i=$t.IndexOf(":")
    if($i -lt 1) { continue }
    $k=$t.Substring(0,$i).Trim()
    $v=$t.Substring($i+1).Trim().Trim('"').Trim("'")
    $h[$k]=$v
  }
  return $h
}

function Get-PrivateDirectory([hashtable]$Config) {
  if ($Config.ContainsKey("private_dir") -and $Config["private_dir"]) {
    return $Config["private_dir"]
  }
  return (Join-Path $script:BootstrapRoot "private")
}

function Get-CredentialRecoveryAssets {
  return @(
    "webcodex.env",
    "cloudflared.token",
    "codex-auth.json"
  )
}

function Get-RunnerSshArguments([hashtable]$Config) {
  if (!$Config["proxmox_alias"]) { throw "proxmox_alias missing" }
  return @(
    "-o", "BatchMode=yes",
    "-o", "ConnectTimeout=10",
    "-o", "StrictHostKeyChecking=yes",
    "-J", $Config["proxmox_alias"]
  )
}

function Get-State {
  if(!(Test-Path $script:StatePath)) {
    return [ordered]@{
      schema=1; preflight="pending"; snapshot="pending"; git_preservation="pending";
      independence_test="pending"; readiness_gate="pending"; destroy="pending";
      control_created="pending"; runner_created="pending"; control_configured="pending";
      runner_configured="pending"; webcodex_connected="pending";
      windows_runner_restricted="pending"; verified="pending";
      snapshot_path=$null; ready_for_destruction=$false
    }
  }
  $obj=Get-Content $script:StatePath -Raw | ConvertFrom-Json
  $h=@{}
  foreach($prop in $obj.PSObject.Properties){ $h[$prop.Name]=$prop.Value }
  return $h
}

function Save-StateAtomic([hashtable]$State) {
  $tmp="$script:StatePath.tmp.$PID"
  $json=$State | ConvertTo-Json -Depth 8
  [IO.File]::WriteAllText($tmp,$json,(New-Object Text.UTF8Encoding($false)))
  Move-Item -Force $tmp $script:StatePath
}

function Set-Stage([string]$Name,[string]$Value,[hashtable]$Extra=@{}) {
  $s=Get-State
  $s[$Name]=$Value
  foreach($k in $Extra.Keys){$s[$k]=$Extra[$k]}
  Save-StateAtomic $s
}

function Write-StageLog([string]$Stage,[string]$Message) {
  $ts=(Get-Date).ToUniversalTime().ToString("o")
  $safe=$Message -replace '(?i)(token|secret|password|authorization)=\S+','$1=<redacted>'
  Add-Content -Path (Join-Path $script:LogsDir "$Stage.log") -Value "$ts $safe"
}

function Invoke-Proxmox([string]$Command,[switch]$AllowFailure) {
  $cfg=Get-SimpleConfig
  $alias=$cfg["proxmox_alias"]
  if(!$alias){throw "proxmox_alias missing"}
  $prevEap=$ErrorActionPreference
  $ErrorActionPreference="Continue"
  $out=& ssh.exe -o BatchMode=yes -o ConnectTimeout=10 $alias $Command 2>&1
  $code=$LASTEXITCODE
  $ErrorActionPreference=$prevEap
  if($code -ne 0 -and !$AllowFailure){ throw "Proxmox command failed ($code): $Command :: $($out -join ' ')" }
  return ,$out
}

function Get-ExistingGuestName([hashtable]$Guest) {
  if($Guest.Type -eq "vm"){
    $out=Invoke-Proxmox "qm config $($Guest.Id)" -AllowFailure
    $line=$out | Where-Object {$_ -match '^name:\s*'} | Select-Object -First 1
    if(!$line){return $null}
    return ($line -replace '^name:\s*','').Trim()
  }
  $out=Invoke-Proxmox "pct config $($Guest.Id)" -AllowFailure
  $line=$out | Where-Object {$_ -match '^hostname:\s*'} | Select-Object -First 1
  if(!$line){return $null}
  return ($line -replace '^hostname:\s*','').Trim()
}

function Get-GuestName([hashtable]$Guest) {
  $name=Get-ExistingGuestName $Guest
  if(!$name){throw "$($Guest.Type) $($Guest.Id) is absent"}
  return $name
}

function Test-GuestExists([hashtable]$Guest) {
  return $null -ne (Get-ExistingGuestName $Guest)
}

function Assert-Guest([hashtable]$Guest) {
  $actual=Get-GuestName $Guest
  if($actual -cne $Guest.Name){ throw "Identity mismatch: $($Guest.Type) $($Guest.Id) expected '$($Guest.Name)' got '$actual'" }
  return $true
}

function Assert-AllGuestIdentities {
  foreach($g in $script:Allowlist){
    $actual=Get-ExistingGuestName $g
    if($null -eq $actual){
      $otherType=if($g.Type -eq "vm"){"lxc"}else{"vm"}
      $otherActual=Get-ExistingGuestName @{Id=$g.Id;Type=$otherType;Name=""}
      if($null -ne $otherActual){throw "Identity type mismatch: $($g.Id) expected $($g.Type) '$($g.Name)' got $otherType '$otherActual'"}
    } elseif($actual -cne $g.Name){
      throw "Identity mismatch: $($g.Type) $($g.Id) expected '$($g.Name)' got '$actual'"
    }
  }
  foreach($g in $script:Denylist){ [void](Assert-Guest $g) }
}

function Test-RemoteFile([string]$Path) {
  $out=Invoke-Proxmox "test -f '$Path' && echo YES || echo NO"
  return (($out -join "`n").Trim() -eq "YES")
}
