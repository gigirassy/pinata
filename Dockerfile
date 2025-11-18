# syntax=docker/dockerfile:1.4
FROM --platform=$BUILDPLATFORM golang:1.24.8-alpine AS builder
ARG TARGETOS
ARG TARGETARCH
ARG TARGETVARIANT

WORKDIR /src

# cache go mod download between builds (requires BuildKit)
COPY go.mod go.sum ./
RUN apk add --no-cache ca-certificates git
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .
RUN rm -rf screenies .github

# Static, trimmed, stripped build. Note: removed -gcflags="all=-l"
RUN CGO_ENABLED=0 \
    GOOS=${TARGETOS:-linux} \
    GOARCH=${TARGETARCH:-amd64} \
    go build -trimpath \
      -ldflags="-s -w -extldflags '-static' -buildid=''" \
      -o /pinata ./main.go

FROM scratch AS runtime
# copy binary and CA certs only
COPY --from=builder /pinata /pinata
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt

# unprivileged user (optional)
USER 65532

EXPOSE 8080

ENV GOMEMLIMIT=8MiB \
    GOGC=20

ENTRYPOINT ["/pinata"]
