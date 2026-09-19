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
