FROM --platform=$BUILDPLATFORM golang:1.26-bookworm AS build
RUN apt-get update && apt-get install -y --no-install-recommends patch \
    && rm -rf /var/lib/apt/lists/*
ARG TARGETOS
ARG TARGETARCH
WORKDIR /work
COPY src/shared ./src/shared
COPY src/nas ./src/nas
COPY LICENSE .
WORKDIR /work/src/nas
RUN go mod download && go mod verify
RUN go mod vendor && patch --batch --fuzz=0 --forward -p1 < packaging/awg-timer.patch
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -mod=vendor -trimpath \
    -ldflags="-s -w" -o /out/outernetawg-nas ./cmd/outernetawg-nas
RUN set -eu; mkdir -p /out/licenses && cp /work/LICENSE /out/licenses/OuternetAWG-LICENSE \
    && go list -mod=mod -m -f '{{.Path}} {{.Version}}' all > /out/licenses/go-modules.txt \
    && go list -mod=mod -m -f '{{.Path}}|{{.Dir}}' all > /tmp/modules; \
    while IFS='|' read -r module directory; do \
        test -n "$directory" || continue; \
        name=$(printf '%s' "$module" | tr '/' '_'); \
        for file in "$directory"/LICENSE* "$directory"/COPYING* "$directory"/NOTICE*; do \
            test ! -f "$file" || cp "$file" "/out/licenses/$name-$(basename "$file")"; \
        done; \
    done < /tmp/modules

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends iproute2 \
    && rm -rf /var/lib/apt/lists/* && install -d -m 0700 /run/outernetawg
COPY --from=build /out/outernetawg-nas /usr/local/bin/outernetawg-nas
COPY --from=build /out/licenses /usr/local/share/outernetawg-nas/licenses
COPY src/nas/README.md /usr/local/share/outernetawg-nas/README.md
COPY src/nas/packaging/awg-timer.patch /usr/local/share/outernetawg-nas/awg-timer.patch
LABEL org.opencontainers.image.title="outernetawg-nas" \
    org.opencontainers.image.description="AmneziaWG NAS endpoint, not an internet gateway" \
    org.opencontainers.image.source="https://github.com/nxyexiong/OuternetAWG-client"
USER 0:0
STOPSIGNAL SIGTERM
HEALTHCHECK --interval=30s --timeout=5s --start-period=30s --retries=3 \
    CMD ["outernetawg-nas", "healthcheck"]
ENTRYPOINT ["outernetawg-nas"]
CMD ["run"]
