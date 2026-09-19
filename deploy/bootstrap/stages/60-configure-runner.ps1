. "$PSScriptRoot\..\Common.ps1"
$stage="60-configure-runner"
Write-StageLog $stage "start"
$s=Get-State
if($s.control_configured -ne "success"){throw "control not configured"}
$cfg=Get-SimpleConfig
$ip=($cfg["runner_ip_cidr"] -split "/")[0]
$target="webcodex@$ip"
$ready=$false
for($i=0;$i -lt 60;$i++){
  & ssh.exe -o StrictHostKeyChecking=accept-new $target "true" 2>$null
  if($LASTEXITCODE -eq 0){$ready=$true;break}
  Start-Sleep -Seconds 5
}
if(!$ready){throw "runner SSH not ready"}
& ssh.exe -o StrictHostKeyChecking=accept-new $target "sudo mkdir -p /srv/codex/mirrors /srv/codex/worktrees /srv/codex/runs /srv/codex/cache /srv/codex/tmp; sudo chown -R webcodex:webcodex /srv/codex; sudo apt-get update; sudo DEBIAN_FRONTEND=noninteractive apt-get install -y ca-certificates curl git jq xz-utils" | Out-Null
if($LASTEXITCODE -ne 0){throw "runner base package install failed"}
$r=$cfg["recovery_dir"]
foreach($f in @("node-v20.19.2-linux-x64.tar.xz","webcodex-runner-0.4.1","webcodex-0.4.1","openai-codex-0.155.0.tgz","openai-codex-0.155.0-linux-x64.tgz","codex-auth.json")){ Invoke-Proxmox "scp -q -o StrictHostKeyChecking=accept-new $r/$f webcodex@${ip}:/tmp/$f" | Out-Null }
& ssh.exe -o StrictHostKeyChecking=accept-new $target "sudo tar -C /usr/local --strip-components=1 -xJf /tmp/node-v20.19.2-linux-x64.tar.xz; sudo install -m 755 /tmp/webcodex-runner-0.4.1 /usr/local/bin/webcodex-runner; sudo install -m 755 /tmp/webcodex-0.4.1 /usr/local/bin/webcodex; sudo npm install -g /tmp/openai-codex-0.155.0.tgz /tmp/openai-codex-0.155.0-linux-x64.tgz; mkdir -p ~/.codex; install -m 600 /tmp/codex-auth.json ~/.codex/auth.json; rm -f /tmp/node-v20.19.2-linux-x64.tar.xz /tmp/webcodex* /tmp/openai-codex* /tmp/codex-auth.json" | Out-Null
if($LASTEXITCODE -ne 0){throw "runner offline tool installation failed"}
Set-Stage "runner_configured" "success"
Write-StageLog $stage "success"