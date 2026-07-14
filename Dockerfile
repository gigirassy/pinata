FROM codeberg.org/gigirassy/thc:latest AS thc

FROM --platform=$BUILDPLATFORM kgrv/golang AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /src
COPY go.mod ./
RUN apk add --no-cache ca-certificates git && go mod download
RUN go env -w GOPROXY=direct


COPY . .

RUN CGO_ENABLED=0 \
    GOOS=${TARGETOS:-linux} \
    GOARCH=${TARGETARCH:-amd64} \
    go build -trimpath \
      -ldflags="-s -w -extldflags '-static' -buildid=''" \
      -o /pinata ./main.go


FROM scratch AS runtime

USER 1000:1000

COPY --from=builder /pinata /pinata
COPY --from=thc /thc /thc
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt

ENV GOMEMLIMIT=15MiB \
    GOGC=20 \
    THC_PORT=8080

EXPOSE 8080

ENTRYPOINT ["/pinata"]

HEALTHCHECK --interval=5s --timeout=5s --start-period=10s --retries=5 CMD ["/thc"]