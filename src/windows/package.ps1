#Requires -Version 7.0
[CmdletBinding()]
param(
    [ValidateSet('x64', 'arm64')][string]$Architecture = 'x64',
    [ValidatePattern('^\d+\.\d+\.\d+(\.\d+)?$')][string]$Version = '1.0.0',
    [switch]$SkipBuild
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$Architecture = $Architecture.ToLowerInvariant()
$root = $PSScriptRoot
$release = Join-Path $root "dist\win-$Architecture"
$output = Join-Path $root 'dist\installers'
$cache = Join-Path $root '.cache'
$compilerVersion = '6.7.3'
$compilerPublisher = 'CN=Pyrsys B\.V\.,'
$compilerDirectory = Join-Path $cache "inno-setup-$compilerVersion"
$compiler = Join-Path $compilerDirectory 'ISCC.exe'
New-Item -ItemType Directory -Force -Path $cache, $output | Out-Null

if (-not $SkipBuild) {
    & "$root\build.ps1" -Architecture $Architecture
    if ($LASTEXITCODE -ne 0) { throw 'The client build failed.' }
}

$manifest = Join-Path $release 'SHA256SUMS'
if (-not (Test-Path -LiteralPath $manifest)) { throw 'Build the client first, or omit -SkipBuild.' }
$files = [Collections.Generic.HashSet[string]]::new([StringComparer]::OrdinalIgnoreCase)
$entries = [Collections.Generic.List[string]]::new()
foreach ($line in Get-Content -LiteralPath $manifest) {
    if ($line -notmatch '^([0-9a-f]{64})  (.+)$') { throw 'Invalid release manifest.' }
    $hash, $relative = $Matches[1], $Matches[2]
    $path = [IO.Path]::GetFullPath((Join-Path $release $relative))
    if (-not $path.StartsWith($release + '\', [StringComparison]::OrdinalIgnoreCase) -or
        $relative -match '[:";{}\r\n]' -or [IO.Path]::IsPathRooted($relative) -or
        -not $files.Add($relative)) {
        throw "Unsafe or duplicate manifest path: $relative"
    }
    $item = Get-Item -LiteralPath $path
    if ($item.PSIsContainer -or ($item.Attributes -band [IO.FileAttributes]::ReparsePoint)) {
        throw "Release payload must contain regular files: $relative"
    }
    if ((Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash -ne $hash) {
        throw "Release checksum mismatch: $relative. Rebuild the client."
    }
    $parent = [IO.Path]::GetDirectoryName($relative)
    $destination = if ($parent) { "{app}\$parent" } else { '{app}' }
    $entries.Add(('Source: "{{#PayloadDir}}\{0}"; DestDir: "{1}"; Flags: ignoreversion' -f $relative, $destination))
}
foreach ($required in @('ui\OuternetAWG.exe', 'ui\OuternetAWG.ico', 'service\outernetawg-service.exe', 'service\wintun.dll',
    'install.ps1', 'uninstall.ps1', 'README.md', 'LICENSE')) {
    if (-not $files.Contains($required)) { throw "Required installer payload is missing: $required" }
}
foreach ($script in @('install.ps1', 'uninstall.ps1')) {
    if ((Get-FileHash -LiteralPath "$root\$script").Hash -ne (Get-FileHash -LiteralPath "$release\$script").Hash) {
        throw "Release contains an outdated $script. Rebuild without -SkipBuild."
    }
}

$image = [IO.File]::ReadAllBytes("$release\service\outernetawg-service.exe")
$machine = [BitConverter]::ToUInt16($image, [BitConverter]::ToInt32($image, 0x3c) + 4)
$expectedMachine = if ($Architecture -eq 'x64') { 0x8664 } else { 0xaa64 }
if ($machine -ne $expectedMachine) { throw 'The service architecture does not match the requested installer.' }
$runtime = Get-Content -LiteralPath "$release\ui\OuternetAWG.runtimeconfig.json" -Raw | ConvertFrom-Json
if ($runtime.runtimeOptions.tfm -ne 'net8.0' -or -not $runtime.runtimeOptions.includedFrameworks) {
    throw 'The installer requires the self-contained .NET 8 UI, not a framework-dependent build.'
}

if (-not (Test-Path -LiteralPath $compiler)) {
    $bootstrap = Join-Path $cache "innosetup-$compilerVersion.exe"
    if (-not (Test-Path -LiteralPath $bootstrap)) {
        Invoke-WebRequest -Uri "https://github.com/jrsoftware/issrc/releases/download/is-6_7_3/innosetup-$compilerVersion.exe" -OutFile $bootstrap
    }
    $expectedHash = '9c73c3bae7ed48d44112a0f48e66742c00090bdb5bef71d9d3c056c66e97b732'
    if ((Get-FileHash -LiteralPath $bootstrap -Algorithm SHA256).Hash -ne $expectedHash) {
        throw "Inno Setup checksum mismatch. Remove only $bootstrap and retry."
    }
    $signature = Get-AuthenticodeSignature -LiteralPath $bootstrap
    if ($signature.Status -ne 'Valid' -or $signature.SignerCertificate.Subject -notmatch $compilerPublisher) {
        throw 'The Inno Setup compiler download is not signed by the expected publisher.'
    }
    # Upstream portable mode suppresses registry integration, shortcuts and uninstall registration.
    $arguments = "/PORTABLE=1 /CURRENTUSER /VERYSILENT /SUPPRESSMSGBOXES /NORESTART /NOICONS /DIR=`"$compilerDirectory`""
    $process = Start-Process -FilePath $bootstrap -ArgumentList $arguments -Wait -PassThru
    if ($process.ExitCode -ne 0) { throw "Portable compiler setup failed with exit code $($process.ExitCode)." }
}
$compilerSignature = Get-AuthenticodeSignature -LiteralPath $compiler
if ($compilerSignature.Status -ne 'Valid' -or $compilerSignature.SignerCertificate.Subject -notmatch $compilerPublisher) {
    throw 'The cached Inno Setup compiler signature is invalid.'
}
$include = Join-Path $cache "payload-$Architecture.iss"
$entries | Set-Content -LiteralPath $include -Encoding utf8
$filename = "OuternetAWG-$Version-windows-$Architecture-setup"
$log = Join-Path $output "$filename.build.log"
& $compiler /Qp "/DAppVersion=$Version" "/DTargetArch=$Architecture" "/DPayloadDir=$release" `
    "/DPayloadInclude=$include" "/DInstallerOutput=$output" "/DInstallerFilename=$filename" `
    "$root\installer\OuternetAWG.iss" 2>&1 | Tee-Object -FilePath $log
if ($LASTEXITCODE -ne 0) { throw "EXE installer compilation failed. See $log." }

$installer = Join-Path $output "$filename.exe"
if (-not (Test-Path -LiteralPath $installer)) { throw 'The compiler did not produce the expected installer.' }
$hash = (Get-FileHash -LiteralPath $installer -Algorithm SHA256).Hash.ToLowerInvariant()
"$hash  $filename.exe" | Set-Content -LiteralPath "$installer.sha256" -Encoding ascii
Write-Host "Complete offline installer: $installer"
Write-Host 'The installer is not Authenticode-signed. No OuternetAWG service was installed or started.'
