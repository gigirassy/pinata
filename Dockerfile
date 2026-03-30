FROM --platform=$BUILDPLATFORM chimeralinux/chimera:latest AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /src
COPY go.mod ./
RUN apk add --no-cache ca-certificates git go && go mod download

COPY . .

RUN go tool soundcloakctl -nozstd -notable precompress
RUN CGO_ENABLED=0 \
    GOOS=${TARGETOS:-linux} \
    GOARCH=${TARGETARCH:-amd64} \
    go build -trimpath \
      -ldflags="-s -w -extldflags '-static' -buildid=''" \
      -gcflags="all=-l" \
      -o /pinata ./main.go


FROM kgrv/mini:tini AS runtime

COPY --from=builder /pinata /pinata
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt

ENV GOMEMLIMIT=8MiB \
    GOGC=20 \
    GODEBUG=netdns=go

USER mini

EXPOSE 8080

ENTRYPOINT ["tini", "--", "/pinata"]