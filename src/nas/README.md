# OuternetAWG NAS client

A Linux Docker **endpoint**, not an internet gateway. Like the original
Outernet NAS client, it makes the NAS's own DSM, SMB and other services reachable
at its VPN address. It embeds the same AmneziaWG Go engine as the other editions
and imports a standard OuternetAWG portal profile. No NAS account login to the
portal, shared server secret, kernel AWG module or management/node changes are
required.

```text
VPN member -> same OuternetAWG node -> NAS VPN address -> NAS services
```

The default target is **linux/amd64**, including Synology DS224+ / DSM 7.2;
**linux/arm64** is also supported. Actual DSM deployment requires a working
Linux TUN device and Container Manager.

## Behavior and requirements

- Create a **dedicated device** in the portal and export its profile for the
  same node used by the clients accessing the NAS. Do not reuse the gateway or
  Windows device profile: sharing a key makes their endpoints displace each other.
- The NAS uses the device's assigned IPv4 `/32`. Its account quota and usage
  remain authoritative on management/the node. The NAS has no independent quota
  ledger; session traffic counters are informational.
- Only the configured VPN subnet is routed through the tunnel: **10.110.0.0/16**
  by default. Set the real management subnet explicitly if different; a `/32`
  export cannot reveal the pool's prefix length.
- NAS LAN connectivity, default routes, policy rules, DNS, forwarding settings,
  NAT and firewall rules are left unchanged. This is not a full-tunnel NAS VPN,
  router gateway, LAN bridge, port-forwarder, or IPv6 VPN.
- The client refuses overlapping non-default IPv4 routes in **any table**, an
  existing tunnel name, subnet network/broadcast addresses, or an endpoint inside
  the VPN subnet. Resolve conflicts rather than replacing NAS/Docker routes.
- Decrypted packets must be IPv4, from a host in the VPN subnet, **to the NAS's
  VPN address**. A TUN filter rejects access to its LAN addresses and forwarding
  to other destinations, even if Docker has already enabled host forwarding.
  Outbound packets must originate from the NAS VPN address to the same subnet.
  IPv4 options are rejected to prevent source-routing around this check.
  IPv6 address generation is disabled on the new TUN only.
- The standard strict parser accepts `.conf`, `.vpn`, or `vpn://` text, with one
  peer, one IPv4 `/32`, and keepalive from 1-120 seconds (normally 25). Scripts,
  custom profile routing, multiple peers and IPv6 tunnel addresses are rejected.
  The full-tunnel export is restricted in memory to the configured subnet.
  Profile DNS is not applied; the host retains its own DNS. The profile MTU is
  used only on the tunnel, and the local UDP listen port is chosen automatically.

This exposes NAS services to VPN members allowed by the node and DSM firewall.
Keep DSM/SMB authentication, share permissions, updates and MFA. It does not
automatically allow ports through DSM's firewall or change service bindings.

## Build

Run from the **OuternetAWG-client repository root**:

```powershell
docker build --platform linux/amd64 --file .\src\nas\Dockerfile --tag outernetawg-nas:latest .
```

For an ARM64 NAS, replace `linux/amd64` with `linux/arm64` and set the Compose
platform accordingly. Docker performs the Go build; Go is not needed on the NAS.
The image includes dependency license notices, not profiles or credentials.

The pinned upstream engine has a timer-expiry race: it clears `duration` after
releasing its mutex, racing with reset/deletion during reconnect. Both NAS image
builds verify module checksums, vendor dependencies **inside the build stage**,
then apply `packaging/awg-timer.patch` with no fuzzy matching. The patch moves
that assignment under the existing lock and adds a race regression test; it
does not change AWG packet formats/settings. The patch is included in the image.
No module cache, repository vendor tree, Windows/gateway build or live deployment
is modified. Remove/reassess it when upgrading to an upstream fix.

To transfer without a registry:

```powershell
New-Item -ItemType Directory -Force .\src\nas\artifacts | Out-Null
docker save --output .\src\nas\artifacts\outernetawg-nas.tar outernetawg-nas:latest
```

Import the archive in **Container Manager -> Image -> Add -> Add From File**,
or run `docker load --input outernetawg-nas.tar` on the Linux NAS.
No GitHub release or registry upload is required.

## Deploy on Synology

1. Create a project directory such as `/volume1/docker/outernetawg-nas`.
2. Copy this directory's `compose.yaml` and `.env.example` to it, renaming
   `.env.example` to `.env`. Adjust platform, subnet and profile path if needed.
3. Put the dedicated portal export in `secrets/OuternetAWG.conf`. Restrict the
   project directory to administrators. As root on the NAS:

   ```sh
   chown root:root /volume1/docker/outernetawg-nas/secrets/OuternetAWG.conf
   chmod 600 /volume1/docker/outernetawg-nas/secrets/OuternetAWG.conf
   ```

   The file must be regular, root-owned, not a symlink, and inaccessible to other
   users. File-backed Compose secrets preserve source ownership/permissions.
   No filesystem-override capability is granted, so incorrect permissions fail
   explicitly. Never put profile contents or private keys in `.env` or arguments.
4. Confirm `/dev/net/tun` is a character device (see below).
5. In **Container Manager -> Project -> Create**, use the project directory and
   existing Compose file. Start the imported image; no build on the NAS is needed.
6. Check logs/status for `connected` and the NAS's assigned VPN address.

Equivalent shell commands in that project directory:

```sh
docker compose up -d
docker compose logs --tail 50 outernetawg-nas
docker compose exec outernetawg-nas outernetawg-nas status
```

Compose uses **host networking** so services running on the NAS itself can receive
VPN traffic. It drops all capabilities except `NET_ADMIN`, maps `/dev/net/tun`,
uses a read-only root filesystem, and stores only nonsecret status in private
tmpfs. Do not use blanket privileged mode, port publishing, or Docker bridge
networking for this deployment. The container does not need the Docker socket.

### Missing TUN device

In DSM **Control Panel -> Task Scheduler**, create a user-defined root task:

```sh
set -eu
if [ ! -d /sys/module/tun ]; then
    /sbin/insmod /lib/modules/tun.ko
fi
if [ ! -e /dev/net/tun ]; then
    mkdir -p /dev/net
    mknod -m 600 /dev/net/tun c 10 200
fi
test -c /dev/net/tun
```

Run it before starting the project. If DSM does not preserve the device/module
across reboot, use a Boot-up triggered task. The device must exist **before**
Docker starts a container mapping it. If needed, append:

```sh
PATH=/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin
export PATH
command -v docker >/dev/null
attempt=0
until docker info >/dev/null 2>&1; do
    attempt=$((attempt + 1))
    if [ "$attempt" -ge 60 ]; then
        echo "Docker did not become ready within 120 seconds" >&2
        exit 1
    fi
    sleep 2
done
docker start outernetawg-nas
```

This optional task explicitly starts the container on every boot, including after
a manual stop; disable the task to keep it stopped. The container cannot load host
modules. If `tun.ko` is absent/incompatible, resolve that prerequisite rather than
granting additional container privileges.

## Access and maintenance

Using the address printed in status (example `10.110.0.8`):

| Service | Example |
| --- | --- |
| DSM HTTPS | `https://10.110.0.8:5001` |
| SMB share from Windows | `\\10.110.0.8\YourShare` |
| Synology Drive / Photos | VPN address and the application's HTTPS port |

SMB must be enabled. Allow the required ports from trusted VPN addresses in DSM's
firewall, and ensure the services are not bound exclusively to the LAN interface.
Use a matching hostname/certificate for HTTPS. Broadcast discovery is not provided.
Access is limited to peers reachable through the selected node, not a node mesh.

`connected` and Docker health require a recent authenticated AWG handshake, not
just an open UDP socket or TUN. No initial handshake within 45 seconds, a handshake
older than three minutes, or engine loss causes teardown and retry with bounded
5-60 second exponential backoff plus jitter. Endpoint IPv4 DNS is resolved again
on each attempt. Keepalives maintain the NAT mapping while idle. A missing
handshake may mean connectivity, keys, revocation or quota; it is not proof of
quota exhaustion. Docker health does not test DSM/SMB authentication or restart
an unhealthy container by itself; the daemon handles reconnects.

Status is refreshed every two seconds; a file older than ten seconds is stale.
It contains the address, endpoint, last handshake, session byte counters, rejected
packet count and errors, never keys. Counters reset on reconnect. Logs are bounded
by Compose rotation; engine error details are redacted.

Stopping/removing the container closes the nonpersistent TUN, removing its VPN
address and subnet route. Normal LAN/internet access stays unchanged. This edition
does **not** install the gateway's persistent kill switch; applications must not
assume that the NAS's normal outbound connection is VPN-protected.

To replace the profile, stop the container, replace the private file, then
recreate it so the read-only secret mount picks up the new inode:

```sh
docker compose down
# Securely replace secrets/OuternetAWG.conf; restore root ownership and mode 600.
docker compose up -d --force-recreate
```

The profile is read once per daemon start. Do not delete the portal device unless
you intend to revoke its identity/address. For image upgrades, import/load the new
image, then use the same stop/recreate procedure. To remove the client, run
`docker compose down`; no host firewall or route restoration script is needed.

## Configuration

| Setting | Default / purpose |
| --- | --- |
| `OUTERNETAWG_PROFILE_FILE` | Container private file, `/run/secrets/outernetawg-profile` |
| `OUTERNETAWG_PROFILE_PATH` | Compose host path, `./secrets/OuternetAWG.conf` |
| `OUTERNETAWG_VPN_SUBNET` | `10.110.0.0/16`; canonical IPv4 `/8` through `/30` |
| `OUTERNETAWG_TUN_NAME` | `oawgnas0`; unused 1-15 character Linux interface name |
| `OUTERNETAWG_PLATFORM` | Compose only, `linux/amd64` or `linux/arm64` |

Run `outernetawg-nas --help` for the three CLI commands: `run`, `status`,
`healthcheck`. There is no web UI, persistent imported-profile database, privileged
control socket, full-internet routing mode, or automatic NAS service configuration.

## Development

Run `go test ./...` and `go vet ./...` in `src/nas`, `src/shared` and
`src/gateway`; run the Windows service tests after shared changes. Ordinary tests
do not modify host networking. Linux integration instructions are in
[`tests/README.md`](tests/README.md); they use isolated synthetic AWG peers and
never production credentials or servers. Real DSM module availability, service
bindings/firewall, reboot ordering and hardware throughput need on-device checks.
