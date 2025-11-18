# syntax=docker/dockerfile:1.4
FROM --platform=$BUILDPLATFORM golang:1.24.8-alpine AS builder
ARG TARGETOS
ARG TARGETARCH
ARG TARGETVARIANT

WORKDIR /src
COPY go.mod go.sum ./
RUN apk add --no-cache ca-certificates git binutils upx \
    && go mod download

COPY . .
RUN rm -rf screenies .github

RUN CGO_ENABLED=0 \
    GOOS=${TARGETOS:-linux} \
    GOARCH=${TARGETARCH:-amd64} \
    go build -trimpath \
      -ldflags="-s -w -extldflags '-static' -buildid=''" \
      -o /pinata ./main.go

# Strip out any remaining symbols
RUN strip /pinata

# Compress the binary
RUN upx -9 /pinata

FROM scratch AS runtime
COPY --from=builder /pinata /pinata
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt

USER 65532
EXPOSE 8080
ENV GOMEMLIMIT=8MiB \
    GOGC=20

ENTRYPOINT ["/pinata"]
