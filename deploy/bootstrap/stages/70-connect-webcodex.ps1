. "$PSScriptRoot\..\Common.ps1"
$stage="70-connect-webcodex"
Write-StageLog $stage "start"
$s=Get-State
if($s.runner_configured -ne "success"){throw "runner not configured"}
$cfg=Get-SimpleConfig
$ip=($cfg["runner_ip_cidr"] -split "/")[0]
$target="webcodex@$ip"
$r=$cfg["recovery_dir"]
$serverUrl=((Invoke-Proxmox "sed -n 's/^WEBCODEX_PUBLIC_URL=//p' $r/webcodex.env | head -1") -join "").Trim()
$token=((Invoke-Proxmox "sed -n 's/^WEBCODEX_TOKEN=//p' $r/webcodex.env | head -1") -join "").Trim()
if(!$serverUrl -or !$token){throw "unable to recover WebCodex URL/token"}
$runnerToml=@"
server_url = "$serverUrl"
token = "$token"
client_id = "codex-runner-01"
owner = "codex-runner-01"
transport = "websocket"
poll_interval_ms = 1000
project_registry_dir = "/home/webcodex/.config/webcodex/v2/project-registry"
[capabilities]
shell = true
file_read = true
file_write = true
git = true
jobs = true
async_jobs = true
async_shell_jobs = true
structured_validation_argv = true
structured_process_argv = true
structured_script_payload = true
structured_execution_jobs = true
lsp_read_only_navigation = true
lsp_call_hierarchy = true
[policy]
allow_raw_shell = true
allow_cwd_anywhere = false
allowed_roots = ["/srv/codex"]
max_timeout_secs = 3600
max_output_bytes = 262144
"@
$tmp=Join-Path $env:TEMP "codex-runner-01.toml"
[IO.File]::WriteAllText($tmp,$runnerToml,(New-Object Text.UTF8Encoding($false)))
& scp.exe -o StrictHostKeyChecking=accept-new $tmp "${target}:/tmp/runner.toml" | Out-Null
Remove-Item -Force $tmp
if($LASTEXITCODE -ne 0){throw "runner config copy failed"}
$unit=@"
[Unit]
Description=WebCodex Runner
After=network-online.target
Wants=network-online.target
[Service]
Type=simple
ExecStart=/usr/local/bin/webcodex-runner --config /home/webcodex/.config/webcodex/v2/runner.toml
Restart=always
RestartSec=5s
WorkingDirectory=/srv/codex
User=webcodex
[Install]
WantedBy=multi-user.target
"@
$tmpUnit=Join-Path $env:TEMP "webcodex-runner.service"
[IO.File]::WriteAllText($tmpUnit,$unit,(New-Object Text.UTF8Encoding($false)))
& scp.exe -o StrictHostKeyChecking=accept-new $tmpUnit "${target}:/tmp/webcodex-runner.service" | Out-Null
Remove-Item -Force $tmpUnit
if($LASTEXITCODE -ne 0){throw "runner unit copy failed"}
& ssh.exe -o StrictHostKeyChecking=accept-new $target "mkdir -p ~/.config/webcodex/v2; install -m 600 /tmp/runner.toml ~/.config/webcodex/v2/runner.toml; rm -f /tmp/runner.toml; sudo install -m 644 /tmp/webcodex-runner.service /etc/systemd/system/webcodex-runner.service; rm -f /tmp/webcodex-runner.service; sudo systemctl daemon-reload; sudo systemctl enable --now webcodex-runner.service" | Out-Null
if($LASTEXITCODE -ne 0){throw "runner service install failed"}
Set-Stage "webcodex_connected" "success"
Write-StageLog $stage "success"