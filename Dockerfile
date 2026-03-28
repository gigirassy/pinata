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


FROM chimeralinux/chimera:latest

RUN apk add --no-cache ca-certificates dnsmasq

COPY --from=builder /pinata /pinata
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt

RUN printf '%s\n' \
'#!/bin/sh' \
'set -eu' \
'' \
'cp /etc/resolv.conf /run/dnsmasq.resolv || true' \
'' \
'dnsmasq --no-daemon \' \
'  --port=5353 \' \
'  --listen-address=127.0.0.1 \' \
'  --cache-size=10000 \' \
'  --neg-ttl=60 \' \
'  --resolv-file=/run/dnsmasq.resolv \' \
'  --log-facility=- &' \
'' \
'sleep 0.2' \
'' \
'exec /pinata' \
> /entrypoint.sh && chmod +x /entrypoint.sh

USER 65532

EXPOSE 8080

ENV GOMEMLIMIT=8MiB \
    GOGC=20 \
    GODEBUG=netdns=go

ENTRYPOINT ["/entrypoint.sh"]