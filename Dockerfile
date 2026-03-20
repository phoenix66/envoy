# ---- build stage ----
# go.mod minimum is 1.25.0, auto-upgraded by golang.org/x/crypto dependency
FROM golang:1.25-alpine AS builder

# Install git for any VCS-stamped dependencies
RUN apk add --no-cache git

WORKDIR /build

# Fetch dependencies first so this layer is cached when only source changes
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Produce a fully static binary: no libc, no CGO, strip debug symbols
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build \
      -ldflags="-s -w -extldflags '-static'" \
      -trimpath \
      -o envoy \
      ./cmd/envoy

# ---- runtime stage ----
FROM alpine:latest

# CA certificates for outbound TLS connections; tzdata for correct timestamps
RUN apk add --no-cache ca-certificates tzdata

# Dedicated non-root user/group (UID/GID 1001)
RUN addgroup -S -g 1001 envoy \
 && adduser  -S -u 1001 -G envoy -H -s /sbin/nologin envoy

# Persistent directories — config is read-only, queue is read-write
RUN mkdir -p /etc/envoy /var/spool/envoy \
 && chown envoy:envoy /var/spool/envoy

COPY --from=builder /build/envoy /usr/local/bin/envoy

# Inbound MTA (25) and client submission (587)
EXPOSE 25 587

VOLUME ["/etc/envoy", "/var/spool/envoy"]

USER envoy

ENTRYPOINT ["envoy"]
CMD ["--config", "/etc/envoy/config.yaml"]
