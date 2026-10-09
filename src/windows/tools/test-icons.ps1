#Requires -Version 5.1
[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string]$ApplicationExe,
    [Parameter(Mandatory = $true)][string]$InstallerExe
)

$ErrorActionPreference = 'Stop'
Add-Type -AssemblyName System.Drawing
$asset = Join-Path (Split-Path -Parent $PSScriptRoot) 'ui\Assets\OuternetAWG.ico'
foreach ($path in @($ApplicationExe, $InstallerExe)) {
    $actualIcon = [Drawing.Icon]::ExtractAssociatedIcon([IO.Path]::GetFullPath($path))
    if ($null -eq $actualIcon) { throw "No executable icon: $path" }
    $expectedIcon = [Drawing.Icon]::new($asset, $actualIcon.Size)
    $actual, $expected = $actualIcon.ToBitmap(), $expectedIcon.ToBitmap()
    try {
        if ($actual.Size -ne $expected.Size) { throw "Icon dimensions differ: $path" }
        for ($y = 0; $y -lt $actual.Height; $y++) {
            for ($x = 0; $x -lt $actual.Width; $x++) {
                $a, $e = $actual.GetPixel($x, $y), $expected.GetPixel($x, $y)
                if ($a.A -eq 0 -and $e.A -eq 0) { continue }
                if ($a.ToArgb() -ne $e.ToArgb()) { throw "Executable icon differs from the shared tray asset at ($x,$y): $path" }
            }
        }
        Write-Host "Shared icon verified: $path"
    }
    finally { $actual.Dispose(); $expected.Dispose(); $actualIcon.Dispose(); $expectedIcon.Dispose() }
}
