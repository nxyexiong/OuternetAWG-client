# NAS Linux integration checks

From the repository root, with Docker's Linux engine running:

```powershell
docker build --file .\src\nas\tests\Dockerfile --tag outernetawg-nas-test .
docker run --rm --cap-add NET_ADMIN --cap-add SYS_ADMIN --security-opt apparmor=unconfined --device /dev/net/tun -e OUTERNET_NAS_NET_TEST=1 outernetawg-nas-test
```

Never add host networking, host filesystem mounts or real profiles to this
container. `SYS_ADMIN` is **test-only**, for private mount/network namespaces;
production Compose grants only `NET_ADMIN`. The daemon subprocess in these tests
drops every other capability. The opt-in runner refuses existing NAS runtime
state and requires a disposable root Docker container.

The race-instrumented suite runs a synthetic AWG node, NAS-like host and second
VPN member, using real TUN devices and obfuscation. It checks peer-to-NAS TCP and
UDP, byte-exact large transfers, packet isolation with host forwarding already
enabled, exclusive TUN ownership, private profile/status permissions, fresh
handshake health, server revocation/recovery, graceful stop, forced termination,
restart and route cleanup. It compares the NAS's default routes, policy rules,
forwarding settings, DNS and unrelated firewall rules before/after.

The test image applies the same pinned-engine timer-lock patch as production and
also runs its concurrent expiry/reset/deletion regression under the race detector.
No upstream race is suppressed or excluded from the integration result.

Revocation deliberately waits for the real three-minute stale-handshake threshold;
allow several minutes. No live NAS, gateway, management service, node or credentials
are contacted. Docker removes the disposable container after completion.
