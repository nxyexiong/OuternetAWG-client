FROM golang:1.26-bookworm
RUN apt-get update && apt-get install --yes --no-install-recommends \
    iproute2 nftables procps systemd && rm -r /var/lib/apt/lists
WORKDIR /work
COPY src/shared ./src/shared
COPY src/gateway ./src/gateway
COPY LICENSE .
WORKDIR /work/src/gateway
ENV GORACE=atexit_sleep_ms=0
RUN go mod download && go build -race -mod=readonly -o /usr/local/bin/outernetawg-gateway ./cmd/outernetawg-gateway
RUN go test ./... && go vet ./... && cd ../shared && go test ./... && go vet ./...
RUN cp packaging/*.service /etc/systemd/system/ && systemd-analyze verify packaging/*.service
CMD ["sh", "tests/run.sh"]
