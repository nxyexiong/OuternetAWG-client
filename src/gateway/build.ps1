#Requires -Version 7.0
[CmdletBinding()]
param(
    [ValidateSet('amd64', 'arm64')][string]$Architecture = 'amd64'
)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$out = Join-Path $PSScriptRoot "dist\linux-$Architecture"
New-Item -ItemType Directory -Force -Path $out, "$out\licenses" | Out-Null
$oldOS, $oldArch, $oldCGO = $env:GOOS, $env:GOARCH, $env:CGO_ENABLED
Push-Location $PSScriptRoot
try {
    $env:GOOS, $env:GOARCH, $env:CGO_ENABLED = 'linux', $Architecture, '0'
    go mod verify
    if ($LASTEXITCODE -ne 0) { throw 'Dependency verification failed.' }
    go build -mod=readonly -trimpath -ldflags '-s -w' -o "$out\outernetawg-gateway" .\cmd\outernetawg-gateway
    if ($LASTEXITCODE -ne 0) { throw 'Gateway build failed.' }
    $modules = & go list -m -f '{{.Path}}|{{.Version}}|{{.Dir}}' all
    if ($LASTEXITCODE -ne 0) { throw 'Could not collect dependency licenses.' }
    [IO.File]::WriteAllText("$out\licenses\go-modules.txt", (($modules | ForEach-Object { ($_.Split('|')[0..1]) -join ' ' }) -join "`n") + "`n")
    foreach ($module in $modules) {
        $parts = $module.Split('|')
        if ($parts.Length -ne 3 -or -not $parts[2]) { continue }
        Get-ChildItem -LiteralPath $parts[2] -File |
            Where-Object { $_.Name -match '^(LICENSE|COPYING|NOTICE)' } |
            ForEach-Object {
                Copy-Item -LiteralPath $_.FullName -Destination (Join-Path "$out\licenses" (($parts[0] -replace '[^\w.-]', '_') + '-' + $_.Name)) -Force
            }
    }
    foreach ($name in 'install.sh', 'gateway.json.example', 'outernetawg-gateway.service', 'outernetawg-gateway-guard.service') {
        $text = [IO.File]::ReadAllText((Join-Path "$PSScriptRoot\packaging" $name)).Replace("`r`n", "`n")
        [IO.File]::WriteAllText((Join-Path $out $name), $text)
    }
    Copy-Item -LiteralPath "$PSScriptRoot\README.md" -Destination $out -Force
    Copy-Item -LiteralPath "$PSScriptRoot\..\..\LICENSE" -Destination "$out\LICENSE" -Force
    [IO.File]::WriteAllText("$out\architecture", "$Architecture`n")
    $manifest = Get-ChildItem -LiteralPath $out -File -Recurse |
        Where-Object { $_.Name -ne 'SHA256SUMS' } |
        Sort-Object FullName |
        ForEach-Object {
            '{0}  {1}' -f (Get-FileHash -LiteralPath $_.FullName -Algorithm SHA256).Hash.ToLowerInvariant(), [IO.Path]::GetRelativePath($out, $_.FullName).Replace('\', '/')
        }
    [IO.File]::WriteAllText("$out\SHA256SUMS", ($manifest -join "`n") + "`n")
    Write-Output "Build ready: $out"
}
finally {
    Pop-Location
    $env:GOOS, $env:GOARCH, $env:CGO_ENABLED = $oldOS, $oldArch, $oldCGO
}
