# syntax=docker/dockerfile:1.4
# beep
FROM --platform=$BUILDPLATFORM kgrv/golang AS builder
USER mini
ARG TARGETOS
ARG TARGETARCH
ARG TARGETVARIANT

WORKDIR /src
COPY go.mod ./
RUN doas apk add --no-cache ca-certificates git && go mod download

COPY . .
RUN CGO_ENABLED=0 \
    GOOS=${TARGETOS:-linux} \
    GOARCH=${TARGETARCH:-amd64} \
    go build -trimpath \
      -ldflags="-s -w -extldflags '-static' -buildid=''" \
      -gcflags="all=-l" \
      -o /pinata ./main.go

FROM scratch AS runtime
COPY --from=builder /pinata /pinata
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
USER 65532
EXPOSE 8080
ENV GOMEMLIMIT=8MiB \
    GOGC=20
ENTRYPOINT ["/pinata"]