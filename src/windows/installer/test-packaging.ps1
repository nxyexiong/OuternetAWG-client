#Requires -Version 5.1
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$root = Split-Path -Parent $PSScriptRoot

foreach ($path in @('package.ps1', 'build.ps1', 'version.ps1', 'install.ps1', 'uninstall.ps1')) {
    $tokens, $parseErrors = $null, $null
    $parsed = [Management.Automation.Language.Parser]::ParseFile(
        (Join-Path $root $path), [ref]$tokens, [ref]$parseErrors)
    if ($parseErrors.Count) { throw "$path contains PowerShell syntax errors: $parseErrors" }
    if ($path -eq 'install.ps1') { $installAST = $parsed }
}

$target = 'C:\Program Files\OuternetAWG'
$script:events = [Collections.Generic.List[string]]::new()
$script:binary = '"C:\Program Files\OuternetAWG\service\outernetawg-service.exe" --owner-sid=S-1-5-21-1-2-3-1001'
$script:registration = [pscustomobject]@{ PathName = $script:binary; StartName = 'LocalSystem' }
$script:service = [pscustomobject]@{ Status = 'Running' }
$script:service | Add-Member ScriptMethod WaitForStatus {
    param($Status, $Timeout)
    if ($Status -ne 'Stopped' -or $Timeout.TotalSeconds -ne 60) { throw 'Unexpected stop wait.' }
    $script:events.Add('wait')
}
$script:service | Add-Member ScriptMethod Dispose { $script:events.Add('dispose') }
$script:returnCode = 0
$script:lastArguments = $null
$script:serviceRegistry = [pscustomobject]@{}

function Get-Service { param($Name, $ErrorAction); return $script:service }
function Get-CimInstance { param($ClassName, $Filter); return $script:registration }
function Stop-Service { param($Name); $script:events.Add('stop') }
function Invoke-CimMethod {
    param($InputObject, $MethodName, $Arguments)
    if ($MethodName -ne 'Change') { throw 'Unexpected service operation.' }
    $script:lastArguments = $Arguments
    return [pscustomobject]@{ ReturnValue = $script:returnCode }
}
function Get-ItemProperty {
    param($LiteralPath)
    if ($LiteralPath -ne 'HKLM:\SYSTEM\CurrentControlSet\Services\OuternetAWG') { throw 'Unexpected registry query.' }
    return $script:serviceRegistry
}

# Load only the lifecycle functions; never invoke the elevated installer entry point.
$names = @('Get-OwnedService', 'Stop-OwnedService', 'Set-ServiceRegistration', 'New-ServiceSecurityDescriptor', 'Get-ServiceSidType')
$definitions = $installAST.FindAll({
    param($node)
    $node -is [Management.Automation.Language.FunctionDefinitionAst]
}, $true)
foreach ($name in $names) {
    $definition = $definitions | Where-Object Name -eq $name
    if (-not $definition) { throw "Missing lifecycle function: $name" }
    . ([scriptblock]::Create($definition.Extent.Text))
}

function Assert-Equal($Expected, $Actual, [string]$Message) {
    if ($Expected -cne $Actual) { throw $Message }
}
function Assert-Rejected([scriptblock]$Action, [string]$Expected) {
    $rejected = $false
    try { & $Action } catch {
        if ($_.Exception.Message -notlike "*$Expected*") { throw }
        $rejected = $true
    }
    if (-not $rejected) { throw "Expected rejection: $Expected" }
}

Assert-Equal $script:service (Get-OwnedService) 'The owned service must be accepted.'
Stop-OwnedService $script:service
Assert-Equal 'stop,wait,dispose' ($script:events -join ',') 'Stop must wait and release the SCM handle.'

foreach ($entry in @{ auto = 'Automatic'; demand = 'Manual'; disabled = 'Disabled' }.GetEnumerator()) {
    Set-ServiceRegistration $script:binary $entry.Key
    Assert-Equal $script:binary $script:lastArguments.PathName 'CIM must preserve executable-path quotes and SID arguments exactly.'
    Assert-Equal $entry.Value $script:lastArguments.StartMode 'Startup mode was not preserved.'
}
Set-ServiceRegistration $script:binary
Assert-Equal 'Manual' $script:lastArguments.StartMode 'New registrations and upgrades must default to Manual startup.'

$sid = 'S-1-5-21-1-2-3-1001'
$descriptor = [Security.AccessControl.RawSecurityDescriptor]::new((New-ServiceSecurityDescriptor $sid))
Assert-Equal 3 $descriptor.DiscretionaryAcl.Count 'Only SYSTEM, Administrators and the authorized user may control the service.'
$ownerACE = @($descriptor.DiscretionaryAcl | Where-Object { $_.SecurityIdentifier.Value -eq $sid })
Assert-Equal 1 $ownerACE.Count 'The authorized Windows user needs one service ACE.'
Assert-Equal 0x35 $ownerACE[0].AccessMask 'The UI may only query configuration/status and start/stop; never change configuration or permissions.'
Assert-Equal 'none' (Get-ServiceSidType) 'An absent ServiceSidType must be recognized as the Windows NONE default.'
foreach ($entry in @{ 0 = 'none'; 1 = 'unrestricted'; 3 = 'restricted' }.GetEnumerator()) {
    $script:serviceRegistry = [pscustomobject]@{ ServiceSidType = $entry.Key }
    Assert-Equal $entry.Value (Get-ServiceSidType) 'The previous SID type must be preserved for rollback.'
}
$script:serviceRegistry = [pscustomobject]@{ ServiceSidType = 2 }
Assert-Rejected { Get-ServiceSidType } 'Unknown Windows service SID type'
$sidSetter = $definitions | Where-Object Name -eq 'Set-ServiceSidType'
Assert-Equal 'unrestricted' $sidSetter.Parameters[0].DefaultValue.SafeGetValue() `
    'Service installation must enable the per-service identity required by WFP.'
$script:returnCode = 5
Assert-Rejected { Set-ServiceRegistration $script:binary } 'code 5'
$script:returnCode = 0

$script:registration.PathName = '"C:\OtherApp\service.exe"'
Assert-Rejected { Get-OwnedService } 'different service'
$script:registration.PathName = $script:binary
$script:registration.StartName = 'AnotherAccount'
Assert-Rejected { Get-OwnedService } 'LocalSystem'
$script:service = $null
Assert-Equal $null (Get-OwnedService) 'A missing service must remain absent.'

Write-Host 'Packaging syntax and mocked service lifecycle checks passed. No host services or network settings were changed.'
