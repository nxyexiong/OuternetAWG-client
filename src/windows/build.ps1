#Requires -Version 7.0
[CmdletBinding()]
param([ValidateSet('x64', 'arm64')][string]$Architecture = 'x64')

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$root = $PSScriptRoot
. "$root\version.ps1"
$version = Get-WindowsClientVersion
$cache = Join-Path $root '.cache'
$output = Join-Path $root "dist\win-$Architecture"
New-Item -ItemType Directory -Force -Path $cache, $output | Out-Null
foreach ($part in @('ui', 'service', 'licenses')) {
    $artifactDirectory = [IO.Path]::GetFullPath((Join-Path $output $part))
    if (Test-Path -LiteralPath $artifactDirectory) {
        $item = Get-Item -LiteralPath $artifactDirectory -Force
        if ($item.Attributes -band [IO.FileAttributes]::ReparsePoint) { throw "Linked build output is not allowed: $artifactDirectory" }
        Remove-Item -LiteralPath $artifactDirectory -Recurse -Force
    }
    New-Item -ItemType Directory -Path $artifactDirectory | Out-Null
}

function Invoke-Checked {
    param([scriptblock]$Command)
    & $Command
    if ($LASTEXITCODE -ne 0) { throw "Build command failed (exit $LASTEXITCODE)." }
}

$archive = Join-Path $cache 'wintun-0.14.1.zip'
$expected = '07c256185d6ee3652e09fa55c0b673e2624b565e02c4b9091c79ca7d2f24ef51'
if (-not (Test-Path -LiteralPath $archive)) {
    Invoke-WebRequest -Uri 'https://www.wintun.net/builds/wintun-0.14.1.zip' -OutFile $archive
}
if ((Get-FileHash -LiteralPath $archive -Algorithm SHA256).Hash -ne $expected) {
    throw "Wintun archive checksum mismatch. Remove only $archive and retry."
}
Expand-Archive -LiteralPath $archive -DestinationPath "$cache\wintun-0.14.1" -Force
$goArch = if ($Architecture -eq 'x64') { 'amd64' } else { 'arm64' }
$dll = Join-Path $cache "wintun-0.14.1\wintun\bin\$goArch\wintun.dll"
$signature = Get-AuthenticodeSignature -LiteralPath $dll
if ($signature.Status -ne 'Valid' -or $signature.SignerCertificate.Subject -notmatch 'CN=WireGuard LLC') {
    throw "Wintun signature validation failed: $($signature.Status). Do not bypass this check."
}
$dllHash = (Get-FileHash -LiteralPath $dll -Algorithm SHA256).Hash.ToLowerInvariant()
Copy-Item -LiteralPath $dll -Destination "$output\service\wintun.dll" -Force
Copy-Item -LiteralPath "$cache\wintun-0.14.1\wintun\LICENSE.txt" -Destination "$output\licenses\Wintun.txt" -Force

$previousOS, $previousArch, $previousCGO = $env:GOOS, $env:GOARCH, $env:CGO_ENABLED
Push-Location "$root\service"
try {
    $env:GOOS = 'windows'
    $env:GOARCH = $goArch
    $env:CGO_ENABLED = '0'
    Invoke-Checked { go mod download }
    Invoke-Checked { go mod verify }
    $flags = "-s -w -X main.ClientVersion=$version -X github.com/nxyexiong/OuternetAWG-client/windows/service/internal/platform.WintunSHA256=$dllHash"
    Invoke-Checked { go build -trimpath -ldflags $flags -o "$output\service\outernetawg-service.exe" .\cmd\outernetawg-service }
    if ([Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString().ToLowerInvariant() -eq $Architecture) {
        $identity = & "$output\service\outernetawg-service.exe" --version
        if ($LASTEXITCODE -ne 0 -or $identity -cnotlike "OuternetAWG Windows service $version;*") {
            throw 'The built Windows service did not report the authoritative client version.'
        }
    }
    [ordered]@{
        version = $version
        architecture = $Architecture
        sha256 = (Get-FileHash -LiteralPath "$output\service\outernetawg-service.exe" -Algorithm SHA256).Hash.ToLowerInvariant()
    } | ConvertTo-Json | Set-Content -LiteralPath "$output\service\build-info.json" -Encoding utf8NoBOM
    $modules = & go list -m -f '{{.Path}}|{{.Version}}|{{.Dir}}' all
    if ($LASTEXITCODE -ne 0) { throw 'Could not collect dependency licenses.' }
    $modules | Set-Content -LiteralPath "$output\licenses\go-modules.txt" -Encoding utf8
    foreach ($module in $modules) {
        $fields = $module.Split('|')
        if ($fields.Length -ne 3 -or -not $fields[2]) { continue }
        $licenseFiles = Get-ChildItem -LiteralPath $fields[2] -File |
            Where-Object { $_.Name -match '^(LICENSE|COPYING|NOTICE)' }
        foreach ($license in $licenseFiles) {
            $name = ($fields[0] -replace '[^\w.-]', '_') + '-' + $license.Name
            Copy-Item -LiteralPath $license.FullName -Destination (Join-Path "$output\licenses" $name) -Force
        }
    }
}
finally {
    Pop-Location
    $env:GOOS, $env:GOARCH, $env:CGO_ENABLED = $previousOS, $previousArch, $previousCGO
}

Invoke-Checked { dotnet publish "$root\ui\OuternetAWG.UI.csproj" "-p:Version=$version" -c Release -r "win-$Architecture" --self-contained true --nologo --verbosity quiet -o "$output\ui" }
Assert-WindowsClientPayloadVersion -Release $output -Version $version -Architecture $Architecture
Copy-Item -LiteralPath "$root\install.ps1", "$root\uninstall.ps1", "$root\README.md" -Destination $output -Force
Copy-Item -LiteralPath "$root\..\..\LICENSE" -Destination "$output\LICENSE" -Force
Copy-Item -LiteralPath "$root\THIRD-PARTY-NOTICES.txt" -Destination "$output\licenses\THIRD-PARTY-NOTICES.txt" -Force
Copy-Item -LiteralPath "$root\service\internal\firewall\LICENSE" -Destination "$output\licenses\OuternetAWG-WFP-MIT.txt" -Force
Invoke-WebRequest -Uri 'https://raw.githubusercontent.com/AvaloniaUI/Avalonia/11.3.22/licence.md' -OutFile "$output\licenses\Avalonia.txt"

$assets = Get-Content -LiteralPath "$root\ui\obj\project.assets.json" -Raw | ConvertFrom-Json -AsHashtable
$nugetNotices = [System.Collections.Generic.List[string]]::new()
foreach ($entry in $assets.libraries.GetEnumerator()) {
    if ($entry.Value.type -ne 'package') { continue }
    foreach ($folder in $assets.packageFolders.Keys) {
        $package = Join-Path $folder ($entry.Value.path.Replace('/', '\'))
        if (-not (Test-Path -LiteralPath $package)) { continue }
        $nuspec = Get-ChildItem -LiteralPath $package -Filter '*.nuspec' | Select-Object -First 1
        if ($null -eq $nuspec) { throw "Missing license metadata: $($entry.Key)" }
        [xml]$metadata = Get-Content -LiteralPath $nuspec.FullName -Raw
        $license = $metadata.SelectSingleNode("//*[local-name()='metadata']/*[local-name()='license']")
        $licenseUrl = $metadata.SelectSingleNode("//*[local-name()='metadata']/*[local-name()='licenseUrl']")
        $nugetNotices.Add($entry.Key)
        if ($null -ne $license) {
            $nugetNotices.Add("License: $($license.InnerText)")
            if ($license.GetAttribute('type') -eq 'file') {
                Copy-Item -LiteralPath (Join-Path $package ($license.InnerText.Replace('/', '\'))) `
                    -Destination (Join-Path "$output\licenses" (($entry.Key -replace '[^\w.-]', '_') + '-LICENSE.txt')) -Force
            }
        }
        if ($null -ne $licenseUrl) { $nugetNotices.Add("License URL: $($licenseUrl.InnerText)") }
        Get-ChildItem -LiteralPath $package -File |
            Where-Object { $_.Name -match 'LICENSE|LICENCE|NOTICE|COPYING' } |
            ForEach-Object {
                $name = ($entry.Key -replace '[^\w.-]', '_') + '-' + $_.Name
                Copy-Item -LiteralPath $_.FullName -Destination (Join-Path "$output\licenses" $name) -Force
            }
        $nugetNotices.Add('')
        break
    }
}
$nugetNotices | Set-Content -LiteralPath "$output\licenses\nuget-packages.txt" -Encoding utf8
$runtimeConfig = Get-Content -LiteralPath "$output\ui\OuternetAWG.runtimeconfig.json" -Raw | ConvertFrom-Json
$runtimeVersion = $runtimeConfig.runtimeOptions.includedFrameworks[0].version
$runtimeFound = $false
foreach ($folder in $assets.packageFolders.Keys) {
    $runtimePath = Join-Path $folder "microsoft.netcore.app.runtime.win-$Architecture\$runtimeVersion"
    if (Test-Path -LiteralPath $runtimePath) {
        Copy-Item -LiteralPath "$runtimePath\LICENSE.TXT" -Destination "$output\licenses\dotnet-LICENSE.TXT" -Force
        Copy-Item -LiteralPath "$runtimePath\THIRD-PARTY-NOTICES.TXT" -Destination "$output\licenses\dotnet-THIRD-PARTY-NOTICES.TXT" -Force
        $runtimeFound = $true
        break
    }
}
if (-not $runtimeFound) { throw 'Cannot locate .NET runtime license files.' }

$manifest = Get-ChildItem -LiteralPath $output -Recurse -File |
    Where-Object { $_.Name -ne 'SHA256SUMS' } |
    Sort-Object FullName |
    ForEach-Object { '{0}  {1}' -f (Get-FileHash -LiteralPath $_.FullName -Algorithm SHA256).Hash.ToLowerInvariant(), [IO.Path]::GetRelativePath($output, $_.FullName) }
$manifest | Set-Content -LiteralPath "$output\SHA256SUMS" -Encoding ascii
Write-Host "Windows client $version release ready: $output"
Write-Host 'No service was installed and no network settings were changed.'
