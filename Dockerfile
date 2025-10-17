# syntax=docker/dockerfile:1.4
#
# Multi-arch build (use BuildKit/docker buildx). Produces a tiny scratch image.
#
FROM --platform=$BUILDPLATFORM golang:1.24.8-alpine AS builder
ARG TARGETOS
ARG TARGETARCH
ARG TARGETVARIANT

WORKDIR /src

# Cache deps
COPY go.mod ./
RUN apk add --no-cache ca-certificates git && go mod download

# Copy sources and build a tiny static binary for the target platform
COPY . .
# Build static binary; CGO disabled so we can use scratch
RUN CGO_ENABLED=0 \
    GOOS=${TARGETOS:-linux} \
    GOARCH=${TARGETARCH:-amd64} \
    go build -trimpath -ldflags="-s -w" -o /pinata ./main.go

# Final stage: minimal runtime
FROM scratch AS runtime
# copy binary
COPY --from=builder /pinata /pinata
# copy CA bundle so TLS works
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt

# run as a non-root numeric UID (no passwd required in scratch)
USER 65532

EXPOSE 8080

ENTRYPOINT ["/pinata"]
