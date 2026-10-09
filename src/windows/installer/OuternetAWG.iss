#ifndef AppVersion
  #error Invoke package.ps1 instead of compiling this file directly.
#endif
#if Ver != EncodeVer(6, 7, 3, 0)
  #error This installer requires the pinned Inno Setup 6.7.3 compiler.
#endif
#if TargetArch == "x64"
  #define AllowedArchitecture "x64os"
#elif TargetArch == "arm64"
  #define AllowedArchitecture "arm64"
#else
  #error Unsupported Windows architecture.
#endif

[Setup]
AppId={{91B4A7E2-747E-4550-96C0-8243451D70AE}
AppName=OuternetAWG
AppVersion={#AppVersion}
AppPublisher=OuternetAWG
AppPublisherURL=https://github.com/nxyexiong/OuternetAWG-client
DefaultDirName={autopf}\OuternetAWG
DisableDirPage=yes
UsePreviousAppDir=no
DisableProgramGroupPage=yes
PrivilegesRequired=admin
ArchitecturesAllowed={#AllowedArchitecture}
ArchitecturesInstallIn64BitMode={#AllowedArchitecture}
MinVersion=10.0.17763
WizardStyle=modern
WizardSizePercent=110
LicenseFile={#PayloadDir}\LICENSE
OutputDir={#InstallerOutput}
OutputBaseFilename={#InstallerFilename}
Compression=lzma2/max
SolidCompression=yes
SetupLogging=yes
SetupMutex=Global\OuternetAWG.Setup
UninstallDisplayIcon={app}\ui\OuternetAWG.exe
UninstallDisplayName=OuternetAWG
VersionInfoVersion={#AppVersion}
SetupIconFile={#PayloadDir}\ui\OuternetAWG.ico
CloseApplications=no
RestartApplications=no
ChangesEnvironment=no
DisableReadyPage=no

[Messages]
WelcomeLabel2=This will install OuternetAWG on your computer.%n%nThe installer includes the desktop app, Windows tunnel service, .NET 8 runtime and signed Wintun driver library. No additional downloads are needed.%n%nChoose Quit from the OuternetAWG tray menu before continuing. Your saved configuration is retained.
FinishedLabel=OuternetAWG has been installed. Open it from Start, import a configuration and click Connect.%n%nThe service starts on demand, not with Windows. Closing the window hides it to the tray; Quit disconnects and stops both the service and UI.

[Tasks]
Name: desktopicon; Description: "Create a desktop shortcut"; Flags: unchecked

[Files]
Source: "{#PayloadDir}\install.ps1"; Flags: dontcopy
Source: "{#PayloadDir}\uninstall.ps1"; Flags: dontcopy
#include PayloadInclude
; This runs inside the installation transaction, after every manifest-listed file is present.
Source: "{#PayloadDir}\SHA256SUMS"; DestDir: "{app}"; Flags: ignoreversion; AfterInstall: ConfigureService

[Icons]
Name: "{commonprograms}\OuternetAWG"; Filename: "{app}\ui\OuternetAWG.exe"; WorkingDir: "{app}\ui"
Name: "{commondesktop}\OuternetAWG"; Filename: "{app}\ui\OuternetAWG.exe"; WorkingDir: "{app}\ui"; Tasks: desktopicon

[Run]
Filename: "{app}\ui\OuternetAWG.exe"; Description: "Open OuternetAWG"; Flags: nowait postinstall skipifsilent runasoriginaluser unchecked

[Code]
var
  AuthorizedSID: String;
  Prepared: Boolean;
  ServiceConfigured: Boolean;
  InstallCompleted: Boolean;

function ValidSIDArgument(const Value: String): Boolean;
var
  I: Integer;
begin
  Result := (Value = '') or (Copy(Value, 1, 4) = 'S-1-');
  for I := 5 to Length(Value) do
    if not ((Value[I] >= '0') and (Value[I] <= '9') or (Value[I] = '-')) then
      Result := False;
end;

function PowerShell(const ScriptPath, Arguments: String; var Details: String): Boolean;
var
  ResultCode, I: Integer;
  Output: TExecOutput;
  Started, PreviousRedirection: Boolean;
begin
  Details := '';
  PreviousRedirection := EnableFsRedirection(False);
  try
    Started := ExecAndCaptureOutput(
      ExpandConstant('{sys}\WindowsPowerShell\v1.0\powershell.exe'),
      '-NoLogo -NoProfile -NonInteractive -ExecutionPolicy Bypass -File "' +
        ScriptPath + '" ' + Arguments,
      '', SW_HIDE, ewWaitUntilTerminated, ResultCode, Output);
  finally
    EnableFsRedirection(PreviousRedirection);
  end;
  if not Started then begin
    Details := 'Could not start Windows PowerShell: ' + SysErrorMessage(ResultCode);
    Result := False;
    Exit;
  end;
  for I := 0 to GetArrayLength(Output.StdOut) - 1 do begin
    Log(Output.StdOut[I]);
    if Copy(Output.StdOut[I], 1, 18) = 'OUTERNET_USER_SID=' then
      AuthorizedSID := Copy(Output.StdOut[I], 19, MaxInt);
  end;
  for I := 0 to GetArrayLength(Output.StdErr) - 1 do begin
    Log(Output.StdErr[I]);
    if Length(Details) < 3000 then
      Details := Details + Output.StdErr[I] + #13#10;
  end;
  Result := (ResultCode = 0) and not Output.Error;
  if Result then
    Details := '';
  if not Result and (Details = '') then
    Details := 'Service setup failed (exit code ' + IntToStr(ResultCode) +
      '). Review the installer log and %ProgramData%\OuternetAWG\service.log.';
end;

function SIDParameter: String;
begin
  Result := '';
  if AuthorizedSID <> '' then
    Result := ' -UserSid "' + AuthorizedSID + '"';
end;

function InitializeSetup: Boolean;
begin
  AuthorizedSID := ExpandConstant('{param:USERSID|}');
  Result := ValidSIDArgument(AuthorizedSID);
  if not Result then
    MsgBox('/USERSID must be a Windows account SID, for example S-1-5-21-... .', mbError, MB_OK);
end;

function PrepareToInstall(var NeedsRestart: Boolean): String;
begin
  Result := '';
  if CompareText(WizardDirValue, ExpandConstant('{autopf}\OuternetAWG')) <> 0 then begin
    Result := 'OuternetAWG must be installed in its protected Program Files directory. Remove the /DIR override.';
    Exit;
  end;
  ExtractTemporaryFile('install.ps1');
  ExtractTemporaryFile('uninstall.ps1');
  Prepared := True;
  if not PowerShell(ExpandConstant('{tmp}\install.ps1'),
      '-Mode Prepare -RecoveryFile "' + ExpandConstant('{tmp}\service-recovery.json') + '"' +
      SIDParameter, Result) then
    Exit;
end;

procedure ConfigureService;
var
  Details: String;
begin
  if not PowerShell(ExpandConstant('{app}\install.ps1'),
      '-Mode Configure -NoShortcut' + SIDParameter, Details) then
    RaiseException(Details);
  ServiceConfigured := True;
end;

procedure CurStepChanged(CurStep: TSetupStep);
begin
  if (CurStep = ssPostInstall) and ServiceConfigured then
    InstallCompleted := True;
end;

procedure DeinitializeSetup;
var
  Details: String;
begin
  if Prepared and not InstallCompleted then begin
    if not PowerShell(ExpandConstant('{tmp}\install.ps1'),
        '-Mode Recover -RecoveryFile "' + ExpandConstant('{tmp}\service-recovery.json') + '"',
        Details) then begin
      Log('Service recovery failed: ' + Details);
      MsgBox('Setup was not completed. Service recovery also failed: ' + Details, mbError, MB_OK);
    end;
  end;
end;

procedure CurUninstallStepChanged(CurUninstallStep: TUninstallStep);
var
  Details: String;
begin
  if CurUninstallStep = usUninstall then begin
    if not PowerShell(ExpandConstant('{app}\uninstall.ps1'), '-ServiceOnly', Details) then
      RaiseException('Uninstall stopped before removing files. ' + Details);
  end;
end;
