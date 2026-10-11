#Requires -Version 7.0

function Get-WindowsClientVersion {
    param([string]$Project = (Join-Path $PSScriptRoot 'ui\OuternetAWG.UI.csproj'))

    $output = & dotnet msbuild $Project -nologo -getProperty:Version
    if ($LASTEXITCODE -ne 0) { throw 'Could not evaluate the Windows client project version.' }
    $version = ($output -join "`n").Trim()
    if ($version -cnotmatch '^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(\.(0|[1-9]\d*))?$' -or
        @($version.Split('.') | Where-Object { [decimal]$_ -gt 65534 }).Count) {
        throw "Windows client Version must contain three or four numeric components in 0..65534: $version"
    }
    return $version
}

function Assert-WindowsClientVersion {
    param(
        [Parameter(Mandatory)][string]$Expected,
        [AllowEmptyString()][string]$Actual,
        [Parameter(Mandatory)][string]$Component,
        [switch]$FileVersion
    )

    $matches = $false
    if ($FileVersion) {
        $parsed = $null
        if ([version]::TryParse($Actual, [ref]$parsed)) {
            $expectedParsed = [version]$Expected
            $expectedParts = @($expectedParsed.Major, $expectedParsed.Minor, $expectedParsed.Build, [Math]::Max(0, $expectedParsed.Revision))
            $actualParts = @($parsed.Major, $parsed.Minor, $parsed.Build, [Math]::Max(0, $parsed.Revision))
            $matches = ($expectedParts -join '.') -ceq ($actualParts -join '.')
        }
    }
    elseif ($Actual) {
        $matches = $Actual.Split('+')[0] -ceq $Expected
    }
    if (-not $matches) {
        throw "$Component version mismatch: expected $Expected, found '$Actual'. Rebuild without -SkipBuild."
    }
}

function Get-WindowsServiceBuildVersion {
    param(
        [Parameter(Mandatory)][string]$Binary,
        [Parameter(Mandatory)][ValidateSet('x64', 'arm64')][string]$Architecture
    )

    # Read Go metadata rather than executing a possibly cross-architecture service.
    $output = & go version -m -json $Binary
    if ($LASTEXITCODE -ne 0) { throw 'Could not read the Windows service build metadata.' }
    $info = ($output -join "`n") | ConvertFrom-Json
    if ($info.Path -cne 'github.com/nxyexiong/OuternetAWG-client/windows/service/cmd/outernetawg-service') {
        throw 'Unexpected Windows service build identity.'
    }
    $expectedArch = if ($Architecture -eq 'x64') { 'amd64' } else { 'arm64' }
    foreach ($entry in @{ GOOS = 'windows'; GOARCH = $expectedArch }.GetEnumerator()) {
        $settings = @($info.Settings | Where-Object Key -eq $entry.Key)
        if ($settings.Count -ne 1 -or $settings[0].Value -cne $entry.Value) {
            throw "Windows service $($entry.Key) does not match the requested installer."
        }
    }
    # -trimpath deliberately omits linker flags from Go build metadata. Bind the
    # generated version record to the exact executable instead of removing trimpath.
    $recordPath = Join-Path (Split-Path -Parent $Binary) 'build-info.json'
    if (-not (Test-Path -LiteralPath $recordPath -PathType Leaf)) {
        throw 'Windows service is missing release version metadata. Rebuild without -SkipBuild.'
    }
    $record = Get-Content -LiteralPath $recordPath -Raw | ConvertFrom-Json
    if ($record.architecture -cne $Architecture -or $record.sha256 -cne (Get-FileHash -LiteralPath $Binary -Algorithm SHA256).Hash.ToLowerInvariant()) {
        throw 'Windows service does not match its version metadata. Rebuild without -SkipBuild.'
    }
    return $record.version
}

function Assert-WindowsClientPayloadVersion {
    param(
        [Parameter(Mandatory)][string]$Release,
        [Parameter(Mandatory)][string]$Version,
        [Parameter(Mandatory)][ValidateSet('x64', 'arm64')][string]$Architecture
    )

    foreach ($name in @('OuternetAWG.exe', 'OuternetAWG.dll')) {
        $path = Join-Path $Release "ui\$name"
        if (-not (Test-Path -LiteralPath $path -PathType Leaf)) { throw "Missing versioned UI payload: $path" }
        $info = [Diagnostics.FileVersionInfo]::GetVersionInfo($path)
        Assert-WindowsClientVersion -Expected $Version -Actual $info.ProductVersion -Component "$name product"
        Assert-WindowsClientVersion -Expected $Version -Actual $info.FileVersion -Component "$name file" -FileVersion
    }
    $assembly = [Reflection.AssemblyName]::GetAssemblyName((Join-Path $Release 'ui\OuternetAWG.dll'))
    Assert-WindowsClientVersion -Expected $Version -Actual $assembly.Version.ToString() -Component 'UI assembly' -FileVersion
    $serviceVersion = Get-WindowsServiceBuildVersion -Binary (Join-Path $Release 'service\outernetawg-service.exe') -Architecture $Architecture
    Assert-WindowsClientVersion -Expected $Version -Actual $serviceVersion -Component 'Windows service'
}
