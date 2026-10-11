# OuternetAWG for Windows

A single-config AmneziaWG client. No login, accounts, server discovery, telemetry,
updater, or profile collection. The frameless Avalonia window has symbol-free macOS-style
traffic-light controls, a left navigation panel and connection, configuration and settings views.
It retains the native Windows frame for DWM shadows and Windows 11 rounded corners;
the custom title bar replaces the standard Windows buttons. The connection page fits
without scrolling, including at the minimum window size.
Its icon, status, description and connect button stay centered as one group within the connection card.

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
.\package.ps1 -Architecture x64
.\package.ps1 -Architecture arm64
```

Each command builds the client and produces a single, complete offline installer:
`dist\installers\OuternetAWG-1.1.0-windows-x64-setup.exe` (or `arm64`),
with a `.sha256` checksum and compiler log alongside it. Use `-SkipBuild` to
repackage an existing, verified release; stale service-install scripts or mismatched
UI/service versions are rejected.
The target computer needs neither Go, a .NET SDK/runtime installation, PowerShell 7,
nor an Internet connection. It uses the Windows PowerShell already supplied by Windows.

The **single Windows client version source** is `<Version>` in
`ui\OuternetAWG.UI.csproj`. Change it once for a new version. Both scripts evaluate
that property through MSBuild; there is no separate installer `-Version` override.
The UI label reads the resulting assembly informational version (without the Git
metadata suffix). The Go service receives the same version through a linker value
and exposes it with `--version`; its default `dev` value is not a release version.
The installer name and Windows version information use that same project value.

Builds verify the UI's executable/DLL product, file and assembly versions. A generated
`service\build-info.json` binds the service's version to its architecture and SHA-256,
since Go's `-trimpath` hides linker flags in embedded build metadata. Native builds
also check the service's side-effect-free `--version` output. Packaging verifies all
of this again, including with `-SkipBuild`, before invoking the installer compiler.
The generated record is build output, not another editable version source.

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
pass it to `.\OuternetAWG-1.1.0-windows-x64-setup.exe /USERSID=S-1-5-21-...`
(or `.\install.ps1 -UserSid 'S-1-5-21-...'` for the raw release).
Start the installer normally, rather than from an already elevated terminal, if
you want its optional launch-at-finish action to run without elevation.
No application login is involved.
One user SID and one machine-wide tunnel are supported.

## Language and appearance

**Settings > General** provides Theme and Language. Both default to **System default**.
Theme supports Light and Dark; the system option follows Windows appearance changes.
Language supports English, French, German, Russian, Simplified Chinese, Traditional
Chinese, Japanese and Korean, with each language listed in its own writing system.
The system option follows the current Windows display language; unsupported languages
fall back to English. Chinese regional preferences select the appropriate script.

Selections apply immediately to the open window, status labels, tooltips and tray menu,
without restarting either process or reconnecting the tunnel. Application-generated
errors also retranslate; raw service/Windows diagnostics and user-supplied profile
names remain unchanged so troubleshooting details are preserved. Native file-dialog
buttons are supplied by Windows and use its display language.

Preferences are saved atomically in `%LocalAppData%\OuternetAWG\settings.json`, separately
from the service's encrypted VPN profile. This file contains only `theme` and `language`;
it does not require administrator access. Read/write failures appear in the UI rather
than silently losing preferences. Upgrades and uninstall leave these per-user settings
in place. An unreadable/invalid file is not overwritten until a setting is changed.

Translations live in `ui\Locales\*.json` and are embedded in the app. Every catalog must
have the same keys and format placeholders as `en.json`. Theme brushes live in dedicated
`ui\Themes\Light.axaml` and `Dark.axaml` dictionaries, referenced with dynamic resources;
Fluent controls follow the same active theme variant.

## Routing profiles

**Settings > Route** selects a routing profile independently of the imported AWG
connection configuration. Two read-only profiles are always available:

| Profile | Unmatched destinations | DNS |
| --- | --- | --- |
| **VPN** (initial default) | Through the VPN; off-tunnel access blocked | Imported VPN DNS |
| **Direct** | Normal Windows networking; tunnel remains connected | Unchanged system DNS |

**New** creates a custom profile using the selected profile's base/rules as a starting
point. Enter a unique name, choose VPN or Direct as the base, and enter one IP/CIDR
and action per line. **Save and select** persists and selects it. **Edit** and **Delete**
are available only for custom profiles; deletion asks for confirmation and selects
the built-in VPN profile. Disconnect before any routing changes.

For internet VPN with local LAN access, use a VPN base and:

```text
192.168.0.0/16 direct
```

For NAS-style access without redirecting ordinary internet traffic, use a Direct
base and:

```text
10.110.0.0/16 vpn
```

Direct alone adds no VPN destination routes: inbound VPN service access also needs
a return route, hence the subnet rule in the NAS example. Host firewalls and service
bindings still apply.

Rules accept IPv4/IPv6 IPs or canonical network CIDRs and `vpn`/`direct` actions.
The most-specific matching prefix wins, regardless of line order; unmatched traffic
uses the base. Duplicate destinations, invalid network addresses and conflicting
names are rejected. There are at most 32 custom profiles and 64 rules per profile.
Custom names cannot be VPN or Direct.

The service atomically persists custom profiles and the selected ID in
`%ProgramData%\OuternetAWG\routing.json`, protected by the service directory ACL.
It contains no VPN keys and is independent of `profile.dpapi` and per-user appearance
preferences. Importing another AWG config does not reset routing. Corrupt settings
or failed writes are reported, never silently replaced with defaults.

## Routing and privacy

The imported AWG config still requires full-tunnel AllowedIPs; routing profiles select
the destinations actually routed to the engine. A single policy compiler generates
disjoint VPN destination prefixes and matching Windows Filtering Platform filters.
Direct exceptions are omitted from VPN routes, including nested exceptions. A full
VPN address family uses two `/1` routes rather than replacing the existing `/0`.
More-specific existing routes for VPN-selected destinations are mirrored on Wintun
at lower cost, not deleted or rewritten. Ambiguous zero-cost conflicts fail connection.
Tunnel addresses are assigned as `/32` or `/128`, so an imported address mask cannot
create an implicit subnet route outside the selected policy.

WFP enforces destination and interface together for outbound and inbound flows.
VPN-selected application traffic cannot fall back outside the tunnel while the
service's policy is active; Direct-selected traffic is allowed on normal interfaces.
Loopback and essential DHCP/NDP retain their higher-priority exceptions. The VPN
server is resolved before connecting, receives an endpoint host-route exception if
needed, and the engine's UDP socket is bound to the original uplink.

VPN-based profiles set DNS only on Wintun and restrict ordinary DNS to the imported
resolvers over that interface. A rule excluding a VPN DNS resolver is rejected on
connection rather than leaking or silently overriding the rule. Direct-based profiles
leave system DNS unchanged. LAN IP access does not automatically enable local hostname
resolution; split DNS and multicast discovery are not configured.

The deployed nodes currently provide IPv4 only. VPN-selected IPv6 remains blocked
when there is no IPv6 tunnel address, rather than falling back to Direct. An explicit
IPv6 VPN rule requires an IPv6-enabled connection config; explicit IPv6 Direct rules
are supported. Built-in Direct leaves ordinary IPv6 networking alone.

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

The service runs as LocalSystem and accepts only `status`, `import`, `connect`,
`disconnect` and `save-routing` over `\\.\pipe\OuternetAWG.Control.v1`. Its ACL admits only the
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
IPv4 AllowedIPs in the imported connection config are rejected rather than silently
ignored; routing-profile rules use the separate validated `save-routing` command.
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
Set-Location ..\..\shared
go test ./...
go vet ./...
Set-Location ..\windows\service
Set-Location ..
dotnet test .\tests\OuternetAWG.UI.Tests.csproj
.\installer\test-packaging.ps1
.\installer\test-versioning.ps1
# Optional: briefly show a real native window/tray, using only a fake service:
dotnet run --project .\tests\OuternetAWG.UI.Tests.csproj -- --native-smoke
# Interactive UI review with a fake service; no real service or network changes:
dotnet run --project .\tests\OuternetAWG.UI.Tests.csproj -- --preview
# After packaging, inspect the actual EXE resources without running either executable:
.\tools\test-icons.ps1 -ApplicationExe .\dist\win-x64\ui\OuternetAWG.exe `
    -InstallerExe .\dist\installers\OuternetAWG-1.1.0-windows-x64-setup.exe
```

The preview opens Settings and stores its synthetic routing profiles only in
`tests\bin\Debug\net8.0\preview-routing.json`. Use the tray's Quit command to close it.
It cannot connect a real VPN or edit production routing settings. Tests cover profile
persistence, immutable built-ins, IPC/controller validation, route partitioning,
native WFP filter structure/precedence through an injected recorder, and localized UI
flows. Live Windows packet-path behavior must additionally be exercised on an
isolated Windows test machine before release; these checks intentionally do not
install filters or routes on the development workstation.

Headless UI tests render the connection page at normal/minimum sizes, including
an error banner, to the ignored test output directory. They assert that controls
fit their actual bounds, Close only hides, Open reuses the window, and Quit waits
for disconnect and service shutdown without masking errors. Teardown regressions
cover adapter/endpoint/firewall ordering and repeated closes.
Preference tests cover catalog completeness, regional/system language resolution,
live window/tray/error translation, both theme palettes, minimum-size localized layouts,
atomic persistence, restart restoration and visible storage failures.
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
