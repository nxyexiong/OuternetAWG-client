FROM golang:1.26-bookworm
RUN apt-get update && apt-get install -y --no-install-recommends \
    iproute2 nftables procps util-linux patch && rm -rf /var/lib/apt/lists/*
WORKDIR /work
COPY src/shared ./src/shared
COPY src/nas ./src/nas
WORKDIR /work/src/nas
ENV GORACE=atexit_sleep_ms=0
RUN go mod download && go mod verify && go mod vendor \
    && patch --batch --fuzz=0 --forward -p1 < packaging/awg-timer.patch
RUN go build -race -mod=vendor -o /usr/local/bin/outernetawg-nas ./cmd/outernetawg-nas
RUN go test -mod=vendor ./... && go vet -mod=vendor ./... \
    && go test -mod=vendor -race github.com/amnezia-vpn/amneziawg-go/v3/device -run TestOuternetTimerExpiryAndReset \
    && cd ../shared && go test ./... && go vet ./...
CMD ["sh", "tests/run.sh"]
