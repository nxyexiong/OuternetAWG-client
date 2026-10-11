#Requires -Version 7.0
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$root = Split-Path -Parent $PSScriptRoot
. "$root\version.ps1"

function Assert-Rejected {
    param([scriptblock]$Action, [string]$Expected)
    $rejected = $false
    try { & $Action } catch {
        if ($_.Exception.Message -notlike "*$Expected*") { throw }
        $rejected = $true
    }
    if (-not $rejected) { throw "Expected rejection: $Expected" }
}

$version = Get-WindowsClientVersion
[xml]$project = Get-Content -LiteralPath "$root\ui\OuternetAWG.UI.csproj" -Raw
if ($version -cne $project.Project.PropertyGroup.Version) { throw 'Project version was not authoritative.' }
foreach ($actual in @('3.4.5', '3.4.5+abcdef')) {
    Assert-WindowsClientVersion -Expected '3.4.5' -Actual $actual -Component 'test product'
}
foreach ($actual in @('3.4.5', '3.4.5.0')) {
    Assert-WindowsClientVersion -Expected '3.4.5' -Actual $actual -Component 'test file' -FileVersion
}
foreach ($actual in @('', 'dev', '3.4.4', '3.4.5-preview', '3.4.5.1')) {
    Assert-Rejected { Assert-WindowsClientVersion -Expected '3.4.5' -Actual $actual -Component 'test product' } 'version mismatch'
}
Assert-Rejected { Assert-WindowsClientVersion -Expected '3.4.5' -Actual '3.4.5.1' -Component 'test file' -FileVersion } 'version mismatch'
Assert-Rejected { Assert-WindowsClientVersion -Expected '3.4.5.1' -Actual '3.4.5.0' -Component 'test assembly' -FileVersion } 'version mismatch'

$tokens, $parseErrors = $null, $null
$package = [Management.Automation.Language.Parser]::ParseFile("$root\package.ps1", [ref]$tokens, [ref]$parseErrors)
if ($parseErrors.Count -or @($package.ParamBlock.Parameters | Where-Object { $_.Name.VariablePath.UserPath -eq 'Version' }).Count) {
    throw 'Packaging must not have an independent Version parameter.'
}
foreach ($path in @('build.ps1', 'package.ps1')) {
    $text = Get-Content -LiteralPath "$root\$path" -Raw
    if ($text -notmatch '\$\w*version\s*=\s*Get-WindowsClientVersion' -or $text -notmatch 'Assert-WindowsClientPayloadVersion') {
        throw "$path does not derive and verify its version."
    }
}

$fixture = Join-Path ([IO.Path]::GetTempPath()) ("OuternetAWG-version-" + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $fixture | Out-Null
$binary = Join-Path $fixture 'service.exe'
$record = Join-Path $fixture 'build-info.json'
try {
    [IO.File]::WriteAllText($binary, 'synthetic, non-executable test bytes')
    $script:buildInfo = @{
        Path = 'github.com/nxyexiong/OuternetAWG-client/windows/service/cmd/outernetawg-service'
        Settings = @(@{ Key = 'GOOS'; Value = 'windows' }, @{ Key = 'GOARCH'; Value = 'amd64' })
    }
    function go {
        param([Parameter(ValueFromRemainingArguments)][string[]]$Arguments)
        if (($Arguments[0..2] -join ' ') -cne 'version -m -json') { throw 'Tests must only read binary metadata.' }
        $global:LASTEXITCODE = 0
        $script:buildInfo | ConvertTo-Json -Depth 4
    }
    Assert-Rejected { Get-WindowsServiceBuildVersion -Binary $binary -Architecture x64 } 'missing release version metadata'
    $stamp = @{
        version = '3.4.5'
        architecture = 'x64'
        sha256 = (Get-FileHash -LiteralPath $binary -Algorithm SHA256).Hash.ToLowerInvariant()
    }
    $stamp | ConvertTo-Json | Set-Content -LiteralPath $record -Encoding utf8NoBOM
    $actual = Get-WindowsServiceBuildVersion -Binary $binary -Architecture x64
    if ($actual -cne '3.4.5') { throw 'Incorrect service build version.' }
    Assert-Rejected { Assert-WindowsClientVersion -Expected '3.4.6' -Actual $actual -Component 'service' } 'version mismatch'
    Assert-Rejected { Get-WindowsServiceBuildVersion -Binary $binary -Architecture arm64 } 'GOARCH'
    [IO.File]::WriteAllText($binary, 'different synthetic binary')
    Assert-Rejected { Get-WindowsServiceBuildVersion -Binary $binary -Architecture x64 } 'does not match its version metadata'
}
finally {
    Remove-Item -LiteralPath $binary -Force
    if (Test-Path -LiteralPath $record) { Remove-Item -LiteralPath $record -Force }
    Remove-Item -LiteralPath $fixture
}
Write-Host 'Single-source version, stale payload and cross-architecture metadata checks passed. No service or installer was executed.'
