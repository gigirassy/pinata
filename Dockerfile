# syntax=docker/dockerfile:1.4
FROM --platform=$BUILDPLATFORM tinygo/tinygo:0.39.0 AS builder

ARG TARGETOS
ARG TARGETARCH

WORKDIR /src

# Copy go modules
COPY go.mod ./
# TinyGo uses go modules too
RUN tinygo mod download

COPY . .
RUN rm -rf screenies .github

# Build with TinyGo
RUN tinygo build \
    -o /pinata \
    -target linux/${TARGETARCH} \
    -opt=z \
    -no-debug \
    ./main.go

FROM scratch AS runtime
COPY --from=builder /pinata /pinata
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt

USER 65532
EXPOSE 8080

# Memory tuning
ENV GOMEMLIMIT=8MiB \
    GOGC=20

ENTRYPOINT ["/pinata"]
