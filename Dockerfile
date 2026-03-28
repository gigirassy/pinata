# syntax=docker/dockerfile:1.4

FROM --platform=$BUILDPLATFORM chimeralinux/chimera:latest AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /src
COPY go.mod ./
RUN apk add --no-cache ca-certificates git go && go mod download

COPY . .
RUN CGO_ENABLED=0 \
    GOOS=${TARGETOS:-linux} \
    GOARCH=${TARGETARCH:-amd64} \
    go build -trimpath \
      -ldflags="-s -w -extldflags '-static' -buildid=''" \
      -gcflags="all=-l" \
      -o /pinata ./main.go


FROM chimeralinux/chimera:latest AS runtime

RUN apk add --no-cache ca-certificates dnsmasq

COPY --from=builder /pinata /pinata
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt

RUN mkdir -p /run

# Non-root by default
USER 65532

RUN cat > /entrypoint.sh <<'EOF'
#!/bin/sh
set -eu

UPSTREAM="/run/dnsmasq.resolv"
cp /etc/resolv.conf "$UPSTREAM" || true

dnsmasq \
  --no-daemon \
  --port=5353 \
  --listen-address=127.0.0.1 \
  --cache-size=10000 \
  --neg-ttl=60 \
  --resolv-file="$UPSTREAM" \
  --log-facility=- &

DNSMASQ_PID=$!
trap 'kill $DNSMASQ_PID 2>/dev/null || true' EXIT INT TERM

sleep 0.3

export GODEBUG=netdns=go
export DNSCACHE="127.0.0.1:5353"

exec /pinata
EOF

RUN chmod +x /entrypoint.sh

EXPOSE 8080

ENV GOMEMLIMIT=8MiB \
    GOGC=20

ENTRYPOINT ["/entrypoint.sh"]