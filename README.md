# OuternetAWG client

The Windows client lives in [`src\windows`](src/windows/README.md):
an unprivileged .NET 8/Avalonia UI and a Go/AmneziaWG/Wintun Windows service.
Import one configuration and connect, without an application account or login.

See the Windows README for build, installation, routing, and service details.

The headless Linux gateway lives in [`src\gateway`](src/gateway/README.md):
one Go daemon with embedded AmneziaWG, source-selected LAN forwarding, persistent
fail-closed protection, a root-only CLI and systemd packaging for amd64/arm64.
It imports the same portal profiles; downstream devices share one VPN identity.

Both editions use the strict profile parser in `src\shared`. Networking and
service lifecycle remain platform-specific.
