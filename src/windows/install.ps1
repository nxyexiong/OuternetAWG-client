#Requires -Version 5.1
#Requires -RunAsAdministrator
[CmdletBinding()]
param(
    [string]$UserSid = '',
    [ValidateSet('Install', 'Prepare', 'Configure', 'Recover')][string]$Mode = 'Install',
    [string]$RecoveryFile = '',
    [switch]$NoShortcut
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$source = $PSScriptRoot
if (-not [Environment]::Is64BitProcess) { throw 'Run this script with native 64-bit Windows PowerShell.' }
$target = Join-Path ([Environment]::GetFolderPath('ProgramFiles')) 'OuternetAWG'
$data = Join-Path ([Environment]::GetFolderPath('CommonApplicationData')) 'OuternetAWG'

function Assert-NormalDirectory([string]$Path) {
    if (Test-Path -LiteralPath $Path) {
        $item = Get-Item -LiteralPath $Path -Force
        if (-not $item.PSIsContainer -or ($item.Attributes -band [IO.FileAttributes]::ReparsePoint)) {
            throw "Refusing non-directory or reparse point: $Path"
        }
        $items = @($item) + @(Get-ChildItem -LiteralPath $Path -Recurse -Force)
        foreach ($entry in $items) {
            if ($entry.Attributes -band [IO.FileAttributes]::ReparsePoint) { throw "Refusing a reparse point: $($entry.FullName)" }
            $acl = Get-Acl -LiteralPath $entry.FullName
            $owner = $acl.GetOwner([Security.Principal.SecurityIdentifier]).Value
            if ($owner -ne 'S-1-5-18' -and $owner -ne 'S-1-5-32-544') {
                throw "Existing installation contains an untrusted owner: $($entry.FullName)"
            }
            foreach ($rule in $acl.GetAccessRules($true, $true, [Security.Principal.SecurityIdentifier])) {
                if ($rule.AccessControlType -ne 'Allow') { continue }
                if ($rule.IdentityReference.Value -in @('S-1-5-18', 'S-1-5-32-544', 'S-1-3-0')) { continue }
                $writeMask = [Security.AccessControl.FileSystemRights]'Write,Delete,DeleteSubdirectoriesAndFiles,ChangePermissions,TakeOwnership'
                if ($rule.FileSystemRights -band $writeMask) {
                    throw "Existing installation allows untrusted writes: $($entry.FullName)"
                }
            }
        }
    }
}

function Protect-Directory([string]$Path, [bool]$AllowRead) {
    New-Item -ItemType Directory -Path $Path -Force | Out-Null
    $acl = [Security.AccessControl.DirectorySecurity]::new()
    $acl.SetAccessRuleProtection($true, $false)
    $acl.SetOwner([Security.Principal.SecurityIdentifier]::new('S-1-5-32-544'))
    foreach ($identity in @('S-1-5-18', 'S-1-5-32-544')) {
        $rule = [Security.AccessControl.FileSystemAccessRule]::new(
            [Security.Principal.SecurityIdentifier]::new($identity), 'FullControl',
            'ContainerInherit,ObjectInherit', 'None', 'Allow')
        $acl.AddAccessRule($rule)
    }
    if ($AllowRead) {
        $acl.AddAccessRule([Security.AccessControl.FileSystemAccessRule]::new(
            [Security.Principal.SecurityIdentifier]::new('S-1-5-32-545'), 'ReadAndExecute',
            'ContainerInherit,ObjectInherit', 'None', 'Allow'))
    }
    Set-Acl -LiteralPath $Path -AclObject $acl
    foreach ($entry in Get-ChildItem -LiteralPath $Path -Recurse -Force) {
        $child = if ($entry.PSIsContainer) {
            [Security.AccessControl.DirectorySecurity]::new()
        } else {
            [Security.AccessControl.FileSecurity]::new()
        }
        $child.SetSecurityDescriptorSddlForm($acl.GetSecurityDescriptorSddlForm('All'))
        Set-Acl -LiteralPath $entry.FullName -AclObject $child
    }
}

function Get-OwnedService {
    $service = Get-Service -Name OuternetAWG -ErrorAction SilentlyContinue
    if ($service) {
        $registration = Get-CimInstance -ClassName Win32_Service -Filter "Name='OuternetAWG'"
        if (-not $registration.PathName.StartsWith('"' + "$target\service\outernetawg-service.exe" + '"', [StringComparison]::OrdinalIgnoreCase)) {
            throw 'A different service already uses the OuternetAWG name. Refusing to replace it.'
        }
        if ($registration.StartName -ne 'LocalSystem') {
            throw 'The existing OuternetAWG service does not run as LocalSystem. Refusing to change its account.'
        }
    }
    return $service
}

function Stop-OwnedService($Service) {
    if ($Service) {
        Stop-Service -Name OuternetAWG
        $Service.WaitForStatus('Stopped', [TimeSpan]::FromSeconds(60))
        $Service.Dispose()
    }
}

function Set-ServiceRegistration([string]$BinaryPath, [string]$StartType = 'demand') {
    $startMode = switch ($StartType) {
        'auto' { 'Automatic' }
        'demand' { 'Manual' }
        'disabled' { 'Disabled' }
        default { throw 'Unknown service startup mode.' }
    }
    $registration = Get-CimInstance -ClassName Win32_Service -Filter "Name='OuternetAWG'"
    $result = Invoke-CimMethod -InputObject $registration -MethodName Change `
        -Arguments @{ PathName = $BinaryPath; StartMode = $startMode }
    if ($result.ReturnValue -ne 0) { throw "Updating the service registration failed (Win32_Service code $($result.ReturnValue))." }
}

function New-ServiceSecurityDescriptor([string]$OwnerSid) {
    $account = [Security.Principal.SecurityIdentifier]::new($OwnerSid)
    if (-not $account.IsAccountSid()) { throw 'The service controller must be a Windows account SID.' }
    return 'D:P(A;;CCDCLCSWRPWPDTLOCRSDRCWDWO;;;SY)(A;;CCDCLCSWRPWPDTLOCRSDRCWDWO;;;BA)(A;;CCLCRPWP;;;' + $account.Value + ')'
}

function Get-ServiceSecurityDescriptor {
    $output = & "$env:SystemRoot\System32\sc.exe" sdshow OuternetAWG
    if ($LASTEXITCODE -ne 0) { throw 'Reading the service access permissions failed.' }
    $descriptor = ($output | Where-Object { $_ -match '^[DOGS]:' }) -join ''
    if (-not $descriptor) { throw 'The service access descriptor was empty.' }
    return $descriptor
}

function Set-ServiceSecurityDescriptor([string]$Descriptor) {
    & "$env:SystemRoot\System32\sc.exe" sdset OuternetAWG $Descriptor | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Setting service start/stop permissions failed.' }
}

function Get-ServiceSidType {
    $registration = Get-ItemProperty -LiteralPath 'HKLM:\SYSTEM\CurrentControlSet\Services\OuternetAWG'
    $property = $registration.PSObject.Properties['ServiceSidType']
    # Windows defaults to SERVICE_SID_TYPE_NONE when the value is absent.
    if ($null -eq $property) { return 'none' }
    switch ([int]$property.Value) {
        0 { return 'none' }
        1 { return 'unrestricted' }
        3 { return 'restricted' }
        default { throw 'Unknown Windows service SID type.' }
    }
}

function Set-ServiceSidType([ValidateSet('none', 'unrestricted', 'restricted')][string]$Type = 'unrestricted') {
    & "$env:SystemRoot\System32\sc.exe" sidtype OuternetAWG $Type | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Setting the service identity required by tunnel leak protection failed.' }
    if ((Get-ServiceSidType) -ne $Type) { throw 'Windows did not persist the requested service SID type.' }
}

if ($Mode -eq 'Recover') {
    if (-not $RecoveryFile) { throw 'Recover requires -RecoveryFile.' }
    if (-not (Test-Path -LiteralPath $RecoveryFile)) {
        Write-Host 'No service recovery snapshot exists; no service changes were made.'
        return
    }
    $snapshot = Get-Content -LiteralPath $RecoveryFile -Raw | ConvertFrom-Json
    $current = Get-OwnedService
    if ($snapshot.Existed) {
        if (-not $current) { throw 'The previous service registration disappeared during setup.' }
        Stop-OwnedService $current
        Set-ServiceRegistration $snapshot.BinaryPath $snapshot.StartType
        Set-ServiceSecurityDescriptor $snapshot.SecurityDescriptor
        Set-ServiceSidType $snapshot.SidType
        if ($snapshot.Running) {
            Start-Service -Name OuternetAWG
            (Get-Service -Name OuternetAWG).WaitForStatus('Running', [TimeSpan]::FromSeconds(20))
        }
        Write-Host 'The previous service registration and running state were restored; its tunnel remains disconnected.'
    } elseif ($current) {
        Stop-OwnedService $current
        & "$env:SystemRoot\System32\sc.exe" delete OuternetAWG | Out-Null
        if ($LASTEXITCODE -ne 0) { throw 'Removing the incomplete service registration failed.' }
        Write-Host 'Removed the incomplete service registration.'
    }
    return
}

if ($Mode -eq 'Configure' -and -not [string]::Equals($source, $target, [StringComparison]::OrdinalIgnoreCase)) {
    throw 'Configure can only run from the installed Program Files directory.'
}

if ($Mode -ne 'Prepare') {
    foreach ($name in @('ui\OuternetAWG.exe', 'service\outernetawg-service.exe', 'service\wintun.dll', 'SHA256SUMS')) {
        if (-not (Test-Path -LiteralPath (Join-Path $source $name) -PathType Leaf)) {
            throw 'Run build.ps1 first, then run install.ps1 from the generated dist\win-* directory.'
        }
    }
    $serviceImage = [IO.File]::ReadAllBytes("$source\service\outernetawg-service.exe")
    $peOffset = [BitConverter]::ToInt32($serviceImage, 0x3c)
    $machine = [BitConverter]::ToUInt16($serviceImage, $peOffset + 4)
    $osArchitecture = [Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString()
    if (($osArchitecture -eq 'X64' -and $machine -ne 0x8664) -or
        ($osArchitecture -eq 'Arm64' -and $machine -ne 0xaa64) -or
        $osArchitecture -notin @('X64', 'Arm64')) {
        throw 'Use the release matching the native Windows architecture (x64 or arm64).'
    }
    foreach ($line in Get-Content -LiteralPath "$source\SHA256SUMS") {
        if ($line -notmatch '^([0-9a-f]{64})  (.+)$') { throw 'Invalid release manifest.' }
        $expected, $relative = $Matches[1], $Matches[2]
        $path = [IO.Path]::GetFullPath((Join-Path $source $relative))
        if (-not $path.StartsWith($source.TrimEnd('\') + '\', [StringComparison]::OrdinalIgnoreCase)) {
            throw 'Manifest path escapes the release directory.'
        }
        if ((Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash -ne $expected) { throw "Release checksum mismatch: $relative" }
    }
    $signature = Get-AuthenticodeSignature -LiteralPath "$source\service\wintun.dll"
    if ($signature.Status -ne 'Valid' -or $signature.SignerCertificate.Subject -notmatch 'CN=WireGuard LLC') {
        throw 'Wintun DLL signature is not valid.'
    }
}
Assert-NormalDirectory $target
Assert-NormalDirectory $data
$runningUI = Get-Process -Name OuternetAWG -ErrorAction SilentlyContinue |
    Where-Object { $_.Path -eq "$target\ui\OuternetAWG.exe" }
if ($runningUI) { throw 'Choose Quit from the OuternetAWG tray menu before installing or updating. Closing the window only hides it.' }
$existing = Get-OwnedService
if ($existing) {
    $registration = Get-CimInstance -ClassName Win32_Service -Filter "Name='OuternetAWG'"
    if (-not $UserSid -and $registration.PathName -match '--owner-sid=(S-1-[0-9-]+)') {
        $UserSid = $Matches[1]
    }
}
if (-not $UserSid) { $UserSid = [Security.Principal.WindowsIdentity]::GetCurrent().User.Value }
$sid = [Security.Principal.SecurityIdentifier]::new($UserSid)
if (-not $sid.IsAccountSid()) { throw 'UserSid must identify the Windows user who will run the UI.' }
if ($Mode -eq 'Prepare') {
    if (-not $RecoveryFile) { throw 'Prepare requires -RecoveryFile.' }
    if (-not (Test-Path -LiteralPath $RecoveryFile)) {
        $snapshot = @{ Existed = [bool]$existing; Running = $false; BinaryPath = ''; StartType = 'demand'; SecurityDescriptor = ''; SidType = 'none' }
        if ($existing) {
            $snapshot.Running = $existing.Status -eq 'Running'
            $snapshot.BinaryPath = $registration.PathName
            $snapshot.SecurityDescriptor = Get-ServiceSecurityDescriptor
            $snapshot.SidType = Get-ServiceSidType
            $snapshot.StartType = switch ($registration.StartMode) { 'Auto' { 'auto' } 'Manual' { 'demand' } 'Disabled' { 'disabled' } default { throw 'Unknown existing service startup mode.' } }
        }
        $snapshot | ConvertTo-Json | Set-Content -LiteralPath $RecoveryFile -Encoding UTF8
    }
}
Stop-OwnedService $existing
Protect-Directory $target $true
Protect-Directory $data $false
if ($Mode -eq 'Prepare') {
    Write-Host "OUTERNET_USER_SID=$($sid.Value)"
    return
}
if ($Mode -eq 'Install') {
    foreach ($directory in @('ui', 'service', 'licenses')) {
        Copy-Item -LiteralPath "$source\$directory" -Destination $target -Recurse -Force
    }
    Copy-Item -LiteralPath "$source\uninstall.ps1", "$source\README.md", "$source\LICENSE" -Destination $target -Force
    Protect-Directory $target $true
}
$binary = '"{0}" --owner-sid={1}' -f "$target\service\outernetawg-service.exe", $sid.Value
if ($existing) {
    Set-ServiceRegistration $binary
} else {
    New-Service -Name OuternetAWG -DisplayName 'OuternetAWG Tunnel Service' -BinaryPathName $binary `
        -StartupType Manual -Description 'On-demand OuternetAWG AmneziaWG/Wintun tunnel and protected local control.' | Out-Null
}
Set-ServiceSidType
Set-ServiceSecurityDescriptor (New-ServiceSecurityDescriptor $sid.Value)
& "$env:SystemRoot\System32\sc.exe" failure OuternetAWG reset= 0 actions= none/0 | Out-Null
if ($LASTEXITCODE -ne 0) { throw 'Configuring service recovery failed.' }

if (-not $NoShortcut) {
    $shell = New-Object -ComObject WScript.Shell
    $shortcut = $shell.CreateShortcut((Join-Path ([Environment]::GetFolderPath('CommonPrograms')) 'OuternetAWG.lnk'))
    $shortcut.TargetPath = "$target\ui\OuternetAWG.exe"
    $shortcut.WorkingDirectory = "$target\ui"
    $shortcut.Save()
}
Write-Host "Installed for Windows SID $UserSid. Open OuternetAWG from Start."
Write-Host 'The service is installed with Manual startup and remains stopped until the UI requests it.'
