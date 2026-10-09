#!/bin/sh
set -eu
arch=${1:-amd64}
case "$arch" in amd64|arm64) ;; *) echo "Use amd64 or arm64." >&2; exit 1 ;; esac
here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
out="$here/dist/linux-$arch"
mkdir -p "$out/licenses"
cd "$here"
export GOOS=linux GOARCH="$arch" CGO_ENABLED=0
go mod verify
go build -mod=readonly -trimpath -ldflags '-s -w' -o "$out/outernetawg-gateway" ./cmd/outernetawg-gateway
go list -m -f '{{.Path}} {{.Version}}' all > "$out/licenses/go-modules.txt"
go list -m -f '{{.Path}}|{{.Dir}}' all | while IFS='|' read -r module dir; do
    test -n "$dir" || continue
    prefix=$(printf '%s' "$module" | tr '/ ' '__')
    for file in "$dir"/LICENSE* "$dir"/COPYING* "$dir"/NOTICE*; do
        test -f "$file" || continue
        cp "$file" "$out/licenses/$prefix-$(basename "$file")"
    done
done
cp "$here/packaging/install.sh" "$here/packaging/gateway.json.example" "$here/packaging/outernetawg-gateway.service" "$here/packaging/outernetawg-gateway-guard.service" "$out/"
cp "$here/README.md" "$here/../../LICENSE" "$out/"
printf '%s\n' "$arch" > "$out/architecture"
cd "$out"
find . -type f ! -name SHA256SUMS -print | LC_ALL=C sort | while IFS= read -r file; do sha256sum "$file"; done > SHA256SUMS
echo "Build ready: $out"
