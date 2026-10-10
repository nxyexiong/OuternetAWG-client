#!/bin/sh
set -eu
if [ "${OUTERNET_NAS_NET_TEST:-}" = 1 ]; then
    test -f /.dockerenv || { echo "Use a disposable Docker container." >&2; exit 1; }
    # Only the disposable container's private mount namespace is changed.
    mount -o remount,rw /proc/sys
fi
go test -mod=vendor -race -count=1 -timeout=420s ./...
