# OuternetAWG for Windows

A single-config AmneziaWG client. No login, accounts, server discovery, telemetry,
updater, or profile collection. The frameless Avalonia window has symbol-free macOS-style
traffic-light controls, a left navigation panel and a right connection/configuration view.
It retains the native Windows frame for DWM shadows and Windows 11 rounded corners;
the custom title bar replaces the standard Windows buttons. The connection page fits
without scrolling, including at the minimum window size.

## Build

Requires Windows 10 1809 or later / Windows 11, Go 1.26+, a .NET SDK capable of
targeting .NET 8, and PowerShell 7. Build without administrator privileges:

```powershell
Set-Location C:\UserData\workspace\OuternetAWG-client\src\windows
.\build.ps1
# Optional native ARM64 release:
.\build.ps1 -Architecture arm64
```

The release is `dist\win-x64` (or `dist\win-arm64`), containing:

- `ui\OuternetAWG.exe`: self-contained **.NET 8 + Avalonia**; runs without elevation.
- `service\outernetawg-service.exe`: Go Windows service with **amneziawg-go v3.1.20260828** embedded.
- `service\wintun.dll`: architecture-matched, officially signed **Wintun 0.14.1**.
- Installation scripts, license notices, and `SHA256SUMS`.

The build checks the official Wintun archive's pinned SHA-256 and DLL Authenticode
signature, then embeds the DLL hash in the service. Wintun is loaded only from the
service/application directory or System32, never the current working directory.
The application's own binaries and scripts are not Authenticode-signed.

## Build a complete EXE installer

```powershell
Set-Location C:\UserData\workspace\OuternetAWG-client\src\windows
.\package.ps1 -Architecture x64 -Version 0.1.3
.\package.ps1 -Architecture arm64 -Version 0.1.3
```

Each command builds the client and produces a single, complete offline installer:
`dist\installers\OuternetAWG-0.1.3-windows-x64-setup.exe` (or `arm64`),
with a `.sha256` checksum and compiler log alongside it. Use `-SkipBuild` to
repackage an existing, verified release; stale service-install scripts are rejected.
The target computer needs neither Go, a .NET SDK/runtime installation, PowerShell 7,
nor an Internet connection. It uses the Windows PowerShell already supplied by Windows.

`package.ps1` downloads **Inno Setup 6.7.3** into `.cache`, verifies the pinned
official SHA-256 and its **Pyrsys B.V.** Authenticode signature, and extracts it
using upstream portable mode. It does not register the compiler or change file
associations. The cached compiler's version/signature and every bundled payload
file's checksum are checked before compilation. Packaging does not run the client
installer or modify the host's services, driver, routes or firewall.

The EXE requests administrator permission, installs into the native
`C:\Program Files\OuternetAWG`, registers the LocalSystem service with **Manual** startup,
creates a Start shortcut (optionally a desktop shortcut), and registers an
uninstaller in **Settings > Apps**. The architecture must match native Windows;
the x64 installer deliberately rejects ARM64 instead of installing an incompatible
driver library. Installation leaves the service stopped and does not connect the tunnel.

Upgrades require choosing **Quit** from the tray, stop the existing service before replacing files,
and retain the saved config and authorized user SID unless `/USERSID=...` is
explicitly supplied. Errors from service installation fail setup rather than being
ignored. If setup fails or is cancelled after preparation, it attempts to restore
the previous service registration, access permissions, SID type and running state, or remove a new partial
registration. A disconnected tunnel is not automatically reconnected during recovery.
Uninstall stops/deletes the service before removing installer-owned files, retains
the encrypted config/logs, and leaves the shared Wintun driver alone.

The generated installer is **unsigned** until you sign it with your own code-signing
certificate; Windows may show an unknown-publisher/SmartScreen warning. Wintun's
upstream DLL signature is retained.

The tray, window/taskbar, application EXE and installer EXE all use
`ui\Assets\OuternetAWG.ico`, including native 16-256 pixel frames. The app embeds
the same file as both its Win32 icon and an Avalonia resource; the installer uses
the copy in the published payload. Regenerate this owned artwork with
`tools\generate-icon.ps1` when changing the logo, then rebuild the release.

## Install and use

Normally, double-click the matching `*-setup.exe`, approve UAC, and follow the
wizard. For a raw release directory, the administrator PowerShell method remains:

```powershell
Set-Location C:\UserData\workspace\OuternetAWG-client\src\windows\dist\win-x64
.\install.ps1
```

Open **OuternetAWG** from Start, import a portal-generated `.conf` / `.vpn` file
(or paste native config / `vpn://` text in Configuration), then click **Connect**.
An active adapter alone is not reported as connected: the UI waits for a successful
AmneziaWG handshake. A server that is offline, has revoked the device, or has exhausted
the account's quota may never complete that handshake.

**Close hides the window to the tray**; it does not disconnect or exit the UI.
Minimize keeps the window in the taskbar and also leaves the connection active.
Click the tray icon or choose **Open UI** from its right-click menu to restore
the same window. The menu also provides **Connect/Disconnect** and **Quit**.

**Quit gracefully stops both processes**: while the service is running, the UI first
sends `disconnect` and waits for cleanup to finish. It then sends the Windows Service
Control Manager stop request, waits for Stopped, and exits the UI. If the control
pipe is unavailable, a graceful SCM stop performs the same server-side cleanup;
there is no attempt to send a disconnect request to an already stopped service.
If stopping is denied or times out, the UI remains running, opens its window and
displays the actual shutdown error. Background polling stays paused rather than
replacing it with a misleading "service did not respond" message.
There is no force-kill or "exit anyway" shortcut.

The service is **on demand**, not launched with Windows: opening the UI requests
its startup so saved configs can be read/imported, but it never automatically connects.
Reopening a hidden window does not start another service or polling loop. Service
crash auto-restart actions are disabled. An abnormal UI process termination is not
the Quit action and can leave the independently running service active.

The installer authorizes the Windows SID running it. If elevation uses a *different*
administrator account, obtain your normal user's SID with `whoami /user` first and
pass it to `.\OuternetAWG-0.1.3-windows-x64-setup.exe /USERSID=S-1-5-21-...`
(or `.\install.ps1 -UserSid 'S-1-5-21-...'` for the raw release).
Start the installer normally, rather than from an already elevated terminal, if
you want its optional launch-at-finish action to run without elevation.
No application login is involved.
One user SID and one machine-wide tunnel are supported.

## Routing and privacy

The client requires full-tunnel IPv4 AllowedIPs and installs **both**
`0.0.0.0/1` and **`128.0.0.0/1`** on its Wintun adapter. The existing `/0` routes
are not deleted or rewritten. The VPN server is resolved before connecting; its
selected IP receives a host-route exception through the original uplink, and the
engine's UDP socket is bound to that interface to avoid routing recursion.

DNS is set only on the tunnel adapter. A dynamic Windows Filtering Platform session
permits the service transport, tunnel traffic, loopback and essential DHCP/NDP,
while blocking off-tunnel application traffic and DNS to unconfigured resolvers.
This also blocks direct LAN access that would otherwise win over the `/1` routes.
More-specific routes belonging to other adapters are not removed.

The deployed OuternetAWG nodes currently provide IPv4 only. An imported `::/0`
without an IPv6 tunnel address **does not enable IPv6**; off-tunnel IPv6 is blocked
by WFP instead of leaking. If a future config provides an IPv6 address and full
IPv6 AllowedIPs, the client adds `::/1` and `8000::/1` as well.

Disconnect closes the engine and its owned Wintun adapter. Wintun's adapter removal
also removes the interface's addresses, DNS and routes; the client does not separately
blank its DNS or attempt to delete Windows-generated interface routes. It then
removes its owned physical-uplink endpoint exception and finally releases the dynamic
firewall session, keeping leak protection active throughout teardown. An owned endpoint exception is
nonpersistent, refreshed every 20 seconds, and expires within 90 seconds of a crash.
Existing exact endpoint host routes are left untouched. Service-process termination
releases Wintun and dynamic WFP state: this is **not a persistent, crash-proof kill
switch**. A network change may require disconnect/reconnect to resolve the endpoint
and choose the new uplink; automatic network roaming is not implemented.

## Process boundary

The service runs as LocalSystem and accepts only `status`, `import`, `connect`
and `disconnect` over `\\.\pipe\OuternetAWG.Control.v1`. Its ACL admits only the
installing user SID, SYSTEM and Administrators; remote pipe clients are rejected.
The UI verifies the pipe server PID against the Service Control Manager before
sending config contents and does not grant impersonation privileges.
The service's SCM ACL separately grants that user only query-configuration,
query-status, start and stop rights. It does not grant change-configuration,
delete, or ACL-write rights. This lets the ordinary unelevated UI perform demand
startup and graceful Quit without repeated UAC prompts; SYSTEM and Administrators
retain full service control. Upgrading an older installation applies these permissions.
The installer also enables and verifies `SERVICE_SID_TYPE_UNRESTRICTED` for the
service on both fresh installs and upgrades. This adds the per-service Windows
identity required by the WFP helper to identify the engine's transport traffic;
it does not disable or relax the tunnel's leak protection. LocalSystem identity
alone is insufficient. A missing service SID is rejected before creating a tunnel
adapter, with an explicit instruction to repair using the latest installer.

IPC is a little-endian 32-bit length followed by UTF-8 JSON, capped at 1 MiB.
Requests carry `version: 1`; config text is capped at 128 KiB. No caller-provided
paths, executables, shell commands or wg-quick scripts are executed. One
`[Interface]` and one `[Peer]` are supported. Unknown/duplicate settings and partial
IPv4 routes are rejected rather than silently ignored.
MTU is limited to 1280-1420, packet padding to 256 bytes and junk packets to 1280
bytes. I1-I5 templates accept bounded `b`, `r`, `rc`, `rd` and `t` tags only.

The service encrypts the saved profile using **LocalSystem user-scope DPAPI** and
atomically replaces `%ProgramData%\OuternetAWG\profile.dpapi`. That directory is
restricted to SYSTEM and Administrators. The UI receives only the profile's name,
endpoint, addresses and connection state; it cannot retrieve saved private keys.
DPAPI data is tied to this machine/account and is not a portable profile backup.
Keep your original exported config safe. Private values are not written to logs.

## Development checks

These tests use synthetic configs, in-memory tunnel adapters and a fake service;
they do not install a service or alter the workstation's routing/firewall:

```powershell
Set-Location C:\UserData\workspace\OuternetAWG-client\src\windows\service
go test ./...
go vet ./...
Set-Location ..
dotnet test .\tests\OuternetAWG.UI.Tests.csproj
.\installer\test-packaging.ps1
# Optional: briefly show a real native window/tray, using only a fake service:
dotnet run --project .\tests\OuternetAWG.UI.Tests.csproj -- --native-smoke
# After packaging, inspect the actual EXE resources without running either executable:
.\tools\test-icons.ps1 -ApplicationExe .\dist\win-x64\ui\OuternetAWG.exe `
    -InstallerExe .\dist\installers\OuternetAWG-0.1.3-windows-x64-setup.exe
```

Headless UI tests render the connection page at normal/minimum sizes, including
an error banner, to the ignored test output directory. They assert that controls
fit their actual bounds, Close only hides, Open reuses the window, and Quit waits
for disconnect and service shutdown without masking errors. Teardown regressions
cover adapter/endpoint/firewall ordering and repeated closes.
The native smoke harness checks the real Win32/DWM frame,
rounded-corner preference, layout, tray and lifecycle without contacting the real service.
Tests also cover portal profile decoding, both split-default
halves, IPv6 policy, invalid configs, bounded IPC, DPAPI persistence, lifecycle and
handshake-based connection state, plus UAPI acceptance by the pinned engine.
The packaging checks parse the PowerShell entry points and exercise service ownership,
quoted ImagePath preservation, stop/wait ordering, manual startup, least-privilege
SCM access, service-SID configuration, startup-mode restoration and error
propagation against mock SCM/CIM functions. The EXE itself must still be exercised
through fresh install, upgrade, cancellation/failure recovery and uninstall on a
disposable Windows host before distribution.

An actual administrator-installed tunnel must additionally be checked with a
fresh, authorized portal config: server handshake, public egress IP, DNS behavior,
IPv6 non-bypass, both halves of IPv4 routing, disconnect restoration, and
service-stop/failure recovery. Unit/headless checks do not prove those network
properties on a specific Windows host.

## Operations and removal

Service errors are recorded in `%ProgramData%\OuternetAWG\service.log`.
Connection setup failures are logged with their failing stage, without config
contents or private keys. The file is intentionally readable only by Administrators
and SYSTEM; the authorized user's local status API also reports the current error.
Administrators can inspect `Get-Service OuternetAWG` and stop it with
`Stop-Service OuternetAWG` to disconnect independently of the UI.
Choose **Quit** from the tray before updating, then rerun the release installer;
closing its window is not sufficient. The saved config is retained and the service
remains stopped until the UI requests it again.

For EXE installations, use **Settings > Apps > OuternetAWG > Uninstall**.
Alternatively run the installed `C:\Program Files\OuternetAWG\uninstall.ps1` as administrator;
it delegates to the registered EXE uninstaller when present.
Add `-RemoveConfig` to delete the encrypted config and logs; otherwise they are
retained. Uninstall does not remove the shared Wintun driver used by other apps.
