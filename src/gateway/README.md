# OuternetAWG Linux gateway

A headless, IPv4 Linux gateway for a VPN router or selected LAN devices. One Go
daemon embeds the same pinned AmneziaWG engine as the Windows client. Import a
portal-generated profile; no application login, management registration, database,
DNS server, web UI, kernel AWG module, or separate engine process is required.
Short-lived CLI commands control the daemon through a root-only Unix socket.

## Requirements and topology

Use a dedicated Debian/Ubuntu systemd host, amd64 or arm64, with Linux TUN,
`iproute2`, `nftables`, and `sysctl`. Keep its address stable and its normal default
route pointed at the real upstream router. nftables flow offload is rejected.
Other forwarding firewalls must permit the intended tunnel traffic; this service
never rewrites UFW/firewalld/Docker rules, and its accepts cannot override their drops.

**Router-wide mode:** the VPN router's WAN is `192.168.10.3`, Linux is
`192.168.10.2`, and the upstream router is `192.168.10.1`, on the same LAN.
Set the VPN router's WAN gateway to `.2`, leave its WAN NAT enabled, and protect
`192.168.10.3/32` on Linux. All devices behind that router share its source address.
Use the WAN/source address actually observed by Linux, not the downstream LAN
address. Linux's own default gateway remains `.1`. One Ethernet interface suffices.

**Side-gateway mode:** selected devices use Linux as their default gateway, manually
or through DHCP. Protect their individual `/32`s or a LAN CIDR. The upstream router
keeps its ISP route. Sources must be on the ingress interface's directly attached
IPv4 network; routed downstream subnets without router NAT are not supported yet.
Devices talking directly on the same LAN do not pass through this gateway.

Advertise the imported profile's DNS addresses on the router/clients. `status`
shows them. Do not use an upstream router's DNS proxy that resolves outside the VPN.
The gateway neither changes host DNS nor opens port 53.

**Disable alternative IPv6 Internet routes on the protected router/network.**
The gateway blocks all IPv6 forwarding arriving on its configured ingress, including
devices outside the IPv4 source list. It cannot prevent a client from using another
gateway, upstream router advertisements, another NIC, or another network. For
enforceable protection, constrain those paths on the router/VLAN. The host's own
traffic, including endpoint DNS, intentionally stays on its normal connection.

## Build and install

Go 1.26+ is required only on the build machine. From PowerShell in this directory:

```powershell
.\build.ps1 -Architecture amd64
.\build.ps1 -Architecture arm64
```

On Linux: `sh build.sh amd64` (or `arm64`). Output is a local installation directory
under `dist/linux-amd64` or `dist/linux-arm64`, containing the binary, systemd units,
installer, dependency notices and SHA-256 checksums. There is no gateway release
or versioned archive. Builds do not install or change networking.

Build on the target Linux host, or copy the entire matching build directory over
a trusted channel. Verify its checksums against the trusted build output, then
run from that directory:

```sh
sudo sh install.sh
```

Installation does not start/enable either unit or change networking. It preserves
saved settings, profile and desired connection state. Stop the daemon before
upgrading; its persistent forwarding guard remains installed.

## First setup

Edit a copy of `gateway.json.example`:

```json
{
  "ingress_interface": "eth0",
  "tunnel_interface": "oawg0",
  "protected_sources": ["192.168.10.3/32"]
}
```

One tunnel and 1-64 nonoverlapping canonical IPv4 CIDRs are supported. Interface
names, source ranges, on-link return paths, existing tunnel interfaces, reserved
policy rules/routes and profile/network overlap are validated. Address assignment,
DHCP and the host's upstream route remain the operator's responsibility.

Before changing client/router gateways:

```sh
sudo outernetawg-gateway configure --file gateway.json
sudo systemctl enable --now outernetawg-gateway-guard.service
sudo systemctl enable --now outernetawg-gateway.service
sudo outernetawg-gateway import --file home-gateway.conf --name "Home gateway"
sudo outernetawg-gateway connect
sudo outernetawg-gateway status
```

Create a dedicated device in the management portal and export a profile for the
desired node. `.conf`, `.vpn`, and `vpn://` text are accepted (`--file -` reads
stdin). Never pass private config text as a command-line argument. Imports accept
exactly one IPv4 `/32` address and require `PersistentKeepalive` from 1-120 seconds;
portal profiles use 25. IPv6 server endpoints are not currently supported.
Profile scripts, custom routing and multiple peers are rejected.

Wait for `"state": "connected"` and `"forwarding": true`, then configure the
router/clients. Check their public egress, DNS, IPv6 non-bypass and disconnect
behavior. An open socket or a created TUN is not proof of a successful handshake.

`connect` persists automatic reconnect/startup. `disconnect` persists a pause and
keeps forwarding blocked:

```sh
sudo outernetawg-gateway disconnect
sudo outernetawg-gateway import --file replacement.vpn
sudo outernetawg-gateway connect
```

Network settings changes require stopping the daemon and rerunning `configure`.
During reconfiguration, the old and new protected sources are blocked together
until the new settings have been durably saved. A failed save leaves the union
blocked rather than opening a rollback gap. Restart the daemon afterward.

## Forwarding and protection

The gateway uses ingress-and-source `ip rule` selectors, rather than packet marks.
They express the same policy without reserving mark bits or restoring marks onto
return traffic. Host-originated packets and AWG's UDP transport do not match them.
The main routing table and Windows-style split defaults are not changed.

Protected packets enter table **21110** and are SNATed to the profile's VPN address.
The node authorizes only that `/32`. Connection tracking sends replies back to
the originating LAN device. The tunnel uses the imported MTU; forwarded TCP SYNs
are MSS-clamped in both directions. Related ICMP can return for path-MTU discovery.
Do not lower the router WAN/Ethernet MTU to the tunnel MTU.

Protection uses owned tables `inet outernetawg_gateway` and
`ip outernetawg_gateway_nat`, route protocol **196**, and rule priorities
**11000-11127**. Do not reuse these reservations. No global ruleset, existing
default route, or unrelated table is flushed. There are no broad private-network
exceptions. Tunnel-originated host access and unsolicited forwarding are denied.
ICMP/IPv6 redirects from the managed ingress are blocked even while disconnected.

Forwarding permission is renewed every two seconds only with a recent handshake,
and expires after 15 seconds without renewal. A stale/missing handshake closes the
session and triggers bounded exponential reconnect attempts (5-60 seconds plus
jitter), resolving the endpoint again. Server failure, peer revocation, quota
exhaustion, service stop and process death never enable direct fallback.

The persistent firewall, unreachable default, and terminal prohibit rules remain
when the TUN disappears. Disconnect first denies forwarding, then closes the
engine/TUN. Service stop preserves the desired startup state, unlike an explicit
disconnect. Host forwarding is enabled only after a guard is installed. Relevant
redirect/reverse-path-filter settings are applied; they are not blindly restored
over another administrator's settings on disconnect.

The enabled guard unit restores rules before ordinary network setup, independently
of daemon/profile validity. If saved network settings are unreadable, its emergency
mode blocks **all forwarded traffic** (not host SSH). If even nftables cannot be
used, it attempts to disable IPv4/IPv6 forwarding and reports failure. This is why
a dedicated gateway is recommended. Do not independently enable early forwarding,
flush the owned firewall/policy rules, use network offload, or arrange a boot path
that routes traffic before the guard. No application can protect against a
privileged administrator removing all its controls.

## Status, quota and maintenance

```sh
sudo outernetawg-gateway status
sudo outernetawg-gateway doctor
sudo journalctl -u outernetawg-gateway -u outernetawg-gateway-guard -f
```

Status exposes the endpoint, addresses, DNS, source policy, handshake, current
session upload/download counters, retry time and errors, never saved keys. Session
counters reset on reconnection and are not authoritative quota/billing totals.
All downstream devices share one portal device/account quota. The server node
remains responsible for accounting and enforcement; management/node changes are
not required. A client cannot distinguish exhausted quota from every other
missing-handshake cause, so errors do not pretend to know the account balance.

Downstream clients can initiate permitted connections to other peers on the same
node. They do not receive individual VPN addresses and are not individually
reachable from remote peers. No site-to-site subnet advertisement, inbound port
mapping, per-LAN-device quota, fail-open mode or multi-profile routing is implemented.

Private state lives in `/var/lib/outernetawg-gateway` (0700), files are 0600 and
atomically replaced with fsync. Linux permissions protect profiles; this is not
DPAPI-style encryption at rest. Use disk encryption if required. Back up private
state securely. Control uses `/run/outernetawg-gateway/control.sock`, root-only
permissions, peer credential verification and bounded JSON framing. A process lock
prevents multiple daemons or offline mutation while the daemon owns networking.

To deliberately restore direct forwarding/remove the installation:

```sh
sudo outernetawg-gateway disconnect
sudo systemctl disable --now outernetawg-gateway.service outernetawg-gateway-guard.service
sudo outernetawg-gateway unprotect --confirm-direct-routing
```

Only owned rules are removed. Host sysctls and saved private state remain.
An enabled guard unit will restore protection at the next boot. Remove the two
installed unit files and binary only after the above succeeds, then run
`systemctl daemon-reload`. Keep or securely remove saved state separately.

## Development validation

Run `go test ./...` and `go vet ./...` here and in `src/shared`; run the existing
Windows service tests after changing shared parsing. Ordinary tests use fake
network/engine adapters and never change host networking.

The opt-in container test image and network-namespace checks are under `tests`.
They require an explicitly disposable Linux container, NET_ADMIN/SYS_ADMIN and
TUN. Never enable their opt-in environment variables on a real gateway or VPS.
They cover actual Linux nftables/iproute2, a synthetic AWG server, one-arm NAT,
IPv4/DNS forwarding, IPv6 blocking, revocation/recovery, disconnect/crash/restart,
and owned-rule cleanup. They never contact production management or nodes.
Real hardware/systemd boot ordering, router DHCP/IPv6 configuration and sustained
throughput still need deployment-specific acceptance checks.
