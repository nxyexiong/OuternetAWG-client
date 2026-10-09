#!/bin/sh
set -eu
if [ "$(id -u)" != 0 ]; then
    echo "Run this installer as root." >&2
    exit 1
fi
here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
cd "$here"
for tool in ip nft sysctl systemctl sha256sum install flock; do
    command -v "$tool" >/dev/null || { echo "Missing $tool; install iproute2, nftables and systemd first." >&2; exit 1; }
done
test -c /dev/net/tun || { echo "Linux TUN is unavailable; enable it before installing." >&2; exit 1; }
case "$(uname -m)" in
    x86_64) arch=amd64 ;;
    aarch64|arm64) arch=arm64 ;;
    *) echo "Only Linux amd64 and arm64 are supported." >&2; exit 1 ;;
esac
test "$(cat architecture)" = "$arch" || { echo "This build does not match the host architecture." >&2; exit 1; }
sha256sum --check --strict SHA256SUMS
test ! -L /run/outernetawg-gateway || { echo "Linked runtime directories are not supported." >&2; exit 1; }
install -d -m 0700 /run/outernetawg-gateway
test ! -L /run/outernetawg-gateway/owner.lock || { echo "Linked lock files are not supported." >&2; exit 1; }
exec 9>/run/outernetawg-gateway/owner.lock
flock --nonblock 9 || { echo "The gateway is running; stop it before installing." >&2; exit 1; }
if systemctl is-active --quiet outernetawg-gateway.service; then
    echo "Stop outernetawg-gateway.service before installing or upgrading. Protection will remain installed." >&2
    exit 1
fi
install -d -m 0755 /usr/local/bin /usr/local/share/outernetawg-gateway
install -m 0755 outernetawg-gateway /usr/local/bin/outernetawg-gateway
install -m 0644 outernetawg-gateway.service outernetawg-gateway-guard.service /etc/systemd/system/
install -m 0644 README.md gateway.json.example LICENSE /usr/local/share/outernetawg-gateway/
install -d -m 0755 /usr/local/share/outernetawg-gateway/licenses
for file in licenses/*; do
    test -f "$file" || continue
    install -m 0644 "$file" /usr/local/share/outernetawg-gateway/licenses/
done
systemctl daemon-reload
echo "Installed without starting services or changing networking."
echo "Follow README.md: configure, enable/start the guard and service, import, then connect."
