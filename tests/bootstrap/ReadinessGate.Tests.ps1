$bootstrapRoot = Join-Path $PSScriptRoot "..\..\deploy\bootstrap"
. (Join-Path $bootstrapRoot "Common.ps1")

Describe "Get-PrivateDirectory" {
  It "uses the bootstrap private directory when the runtime config omits private_dir" {
    $config = @{}

    Get-PrivateDirectory $config | Should Be (Join-Path $script:BootstrapRoot "private")
  }

  It "uses a configured private directory when supplied" {
    $config = @{ private_dir = "C:\\CustomPrivate" }

    Get-PrivateDirectory $config | Should Be "C:\\CustomPrivate"
  }
}

Describe "Get-CredentialRecoveryAssets" {
  It "lists only the credentials consumed by the rebuild stages" {
    Get-CredentialRecoveryAssets | Should Be @(
      "webcodex.env",
      "cloudflared.token",
      "codex-auth.json"
    )
  }
}

Describe "rebuild stage guard" {
  It "requires AllowDestruction only for stage 20" {
    $scriptText = Get-Content (Join-Path $bootstrapRoot "rebuild.ps1") -Raw

    $scriptText | Should Match 'if\(\$s\.N -eq 20 -and !\$AllowDestruction\)'
  }
}

Describe "V3 migration freeze" {
  It "blocks the superseded V2 rebuild entrypoint when the freeze marker exists" {
    $scriptText = Get-Content (Join-Path $bootstrapRoot "rebuild.ps1") -Raw

    $scriptText | Should Match 'v3-plan-freeze\.json'
    $scriptText | Should Match 'V2 bootstrap is frozen'
  }

  It "persists V3 supersession without deleting existing bootstrap state" {
    $scriptText = Get-Content (Join-Path $bootstrapRoot "Freeze-V2.ps1") -Raw

    $scriptText | Should Match 'PLAN_VERSION'
    $scriptText | Should Match 'OLD_PLAN_SUPERSEDED'
    $scriptText | Should Match 'Save-StateAtomic'
  }
}

Describe "V3 bootstrap target" {
  It "declares V3 as the only supported migration plan" {
    $scriptText = Get-Content (Join-Path $bootstrapRoot "v3\Invoke-V3Migration.ps1") -Raw

    $scriptText | Should Match 'PLAN_VERSION.*v3-final'
    $scriptText | Should Match 'P0.*Freeze'
    $scriptText | Should Match 'P1.*Inventory'
    $scriptText | Should Not Match '20-destroy\.ps1'
  }
}

Describe "Get-RunnerSshArguments" {
  It "uses the configured Proxmox jump host with bounded strict SSH" {
    $config = @{ proxmox_alias = "proxmox-pve" }

    (Get-RunnerSshArguments $config) -join " " | Should Be "-o BatchMode=yes -o ConnectTimeout=10 -o StrictHostKeyChecking=yes -J proxmox-pve"
  }
}

Describe "runner recovery transfer" {
  It "stages recovery assets locally instead of requiring a Proxmox private key" {
    $stageText = Get-Content (Join-Path $bootstrapRoot "stages\\60-configure-runner.ps1") -Raw

    $stageText | Should Match '\$privateDir=Get-PrivateDirectory \$cfg'
    $stageText | Should Match '& scp\.exe .*\$localRecoveryPath '
    $stageText | Should Not Match 'Invoke-Proxmox "scp'
  }
}

Describe "runner Codex CLI installation" {
  It "installs and verifies the pinned platform binary" {
    $stageText = Get-Content (Join-Path $bootstrapRoot "stages\\60-configure-runner.ps1") -Raw

    $stageText | Should Match '/usr/local/lib/node_modules/@openai/codex/vendor/x86_64-unknown-linux-musl/bin/codex'
    $stageText | Should Match 'codex --version'
  }
}
