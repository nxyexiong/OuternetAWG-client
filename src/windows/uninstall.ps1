#Requires -Version 5.1
#Requires -RunAsAdministrator
[CmdletBinding()]
param([switch]$RemoveConfig, [switch]$ServiceOnly)

$ErrorActionPreference = 'Stop'
if (-not [Environment]::Is64BitProcess) { throw 'Run this script with native 64-bit Windows PowerShell.' }
if ($ServiceOnly -and $RemoveConfig) { throw '-ServiceOnly cannot be combined with -RemoveConfig.' }
$target = Join-Path ([Environment]::GetFolderPath('ProgramFiles')) 'OuternetAWG'
$data = Join-Path ([Environment]::GetFolderPath('CommonApplicationData')) 'OuternetAWG'
if (Get-Process -Name OuternetAWG -ErrorAction SilentlyContinue | Where-Object { $_.Path -eq "$target\ui\OuternetAWG.exe" }) {
    throw 'Choose Quit from the OuternetAWG tray menu before uninstalling. Closing the window only hides it.'
}
$registeredUninstaller = Join-Path $target 'unins000.exe'
$usedEXE = $false
if (-not $ServiceOnly -and (Test-Path -LiteralPath $registeredUninstaller)) {
    $process = Start-Process -FilePath $registeredUninstaller -ArgumentList '/NORESTART' -Wait -PassThru
    if ($process.ExitCode -ne 0) { throw "Uninstall was cancelled or failed (exit $($process.ExitCode))." }
    $usedEXE = $true
}
$service = if (-not $usedEXE) { Get-Service -Name OuternetAWG -ErrorAction SilentlyContinue }
if ($service) {
    $registration = Get-CimInstance -ClassName Win32_Service -Filter "Name='OuternetAWG'"
    if (-not $registration.PathName.StartsWith('"' + "$target\service\outernetawg-service.exe" + '"', [StringComparison]::OrdinalIgnoreCase)) {
        throw 'A different service uses the OuternetAWG name. Refusing to delete it.'
    }
    Stop-Service -Name OuternetAWG
    $service.WaitForStatus('Stopped', [TimeSpan]::FromSeconds(60))
    $service.Dispose()
    & "$env:SystemRoot\System32\sc.exe" delete OuternetAWG | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Deleting the service failed.' }
}
if ($ServiceOnly) {
    Write-Host 'Tunnel service removed. Installer-owned files and saved configuration were left untouched.'
    return
}
$shortcut = Join-Path ([Environment]::GetFolderPath('CommonPrograms')) 'OuternetAWG.lnk'
if (Test-Path -LiteralPath $shortcut) { Remove-Item -LiteralPath $shortcut }
foreach ($path in $(if (-not $usedEXE) { @($target) } else { @() })) {
    if (Test-Path -LiteralPath $path) {
        $item = Get-Item -LiteralPath $path -Force
        if ($item.Attributes -band [IO.FileAttributes]::ReparsePoint) { throw "Refusing to remove a reparse point: $path" }
        Remove-Item -LiteralPath $item.FullName -Recurse -Force
    }
}
if ($RemoveConfig -and (Test-Path -LiteralPath $data)) {
    $item = Get-Item -LiteralPath $data -Force
    if ($item.Attributes -band [IO.FileAttributes]::ReparsePoint) { throw 'Refusing to remove a linked data directory.' }
    Remove-Item -LiteralPath $item.FullName -Recurse -Force
}
Write-Host 'OuternetAWG removed. The shared Wintun driver is left installed for other applications.'
if (-not $RemoveConfig) { Write-Host "Encrypted config and logs retained in $data. Use -RemoveConfig to remove them." }
