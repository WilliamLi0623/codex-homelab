. "$PSScriptRoot\..\Common.ps1"
$stage="75-restrict-windows-runner"
Write-StageLog $stage "start"
$s=Get-State
if($s.webcodex_connected -ne "success"){throw "webcodex connection stage not successful"}
$workspace="C:\WebCodexWorkspace"
New-Item -ItemType Directory -Force -Path "$workspace\repos","$workspace\worktrees","$workspace\scratch","$workspace\bootstrap" | Out-Null
$cfgFile=Get-ChildItem (Join-Path $env:APPDATA "webcodex") -Recurse -Filter runner.toml -ErrorAction Stop | Where-Object {$_.FullName -match "windows-workstation"} | Select-Object -First 1
if(!$cfgFile){throw "Windows runner config not found"}
$text=Get-Content $cfgFile.FullName -Raw
$text=[regex]::Replace($text,'(?m)^allowed_roots\s*=.*$','allowed_roots = ["C:\\WebCodexWorkspace"]')
Write-Utf8 $cfgFile.FullName $text
Copy-Item -Recurse -Force "$script:BootstrapRoot\*" "$workspace\bootstrap\" -ErrorAction SilentlyContinue
Set-Stage "windows_runner_restricted" "success"
Write-StageLog $stage "config updated; runner restart/re-registration verification required"