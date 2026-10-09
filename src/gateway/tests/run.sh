#!/bin/sh
set -eu
if [ "${OUTERNET_GATEWAY_NET_TEST:-}" = 1 ]; then
    test -f /.dockerenv || { echo "Network checks require a disposable Docker container." >&2; exit 1; }
    # Docker mounts this read-only. Only the test container's mount namespace changes.
    mount -o remount,rw /proc/sys
fi
go test -race -count=1 -timeout=240s ./...
