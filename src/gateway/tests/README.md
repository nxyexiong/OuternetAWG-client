# Disposable Linux validation

Run from the client repository root with Docker Desktop's Linux engine running:

```powershell
docker build --tag outernetawg-gateway-test --file .\src\gateway\tests\Dockerfile .
docker run --rm --cap-add NET_ADMIN --cap-add SYS_ADMIN --security-opt apparmor=unconfined --device /dev/net/tun -e OUTERNET_GATEWAY_NET_TEST=1 outernetawg-gateway-test
```

No host networking, host filesystem mounts, production profiles or cloud resources
are used. SYS_ADMIN is needed only to create private test network namespaces;
production gateway units do not have it. The runner remounts `/proc/sys` writable
inside the test container's private mount namespace so its network namespaces can
set their forwarding sysctls. The opt-in tests refuse existing gateway
state and refuse execution outside a root Docker container.

Four namespaces emulate a switch, a one-arm gateway, a protected LAN client and
an upstream router/synthetic AWG node. A plaintext route deliberately exists so
leakage produces a successful request and fails the assertion. The synthetic node
requires the gateway's exact VPN `/32`, so successful HTTP/UDP also proves SNAT.

Ordinary unit tests, race detection, strict parser checks and systemd unit syntax
validation run without these privileges. Live tests additionally exercise real
nftables/iproute2/TUN, bidirectional AWG traffic, DNS-port UDP, blocked IPv6,
peer revocation/restoration, persistent pause, crash/restart, corrupt boot settings,
and explicit removal without touching unrelated firewall tables.
