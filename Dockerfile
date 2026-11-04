# ============================================================
# thefeed-server — Multi-stage Docker build
# ============================================================
# Run:    docker compose up -d   (see docker-compose.yml)
# Login:  docker compose run -it --rm server --data-dir /data --login-only
# Settings come from /data/thefeed.env, which the server reads itself.
# ============================================================

# ---- Stage 1: Build ----
# On the build machine's platform: Go cross-compiles, so a multi-arch build
# doesn't compile under emulation.
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS builder

RUN apk add --no-cache git ca-certificates tzdata

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Build-time version info (overridable via --build-arg)
ARG VERSION=docker
ARG COMMIT=unknown
ARG DATE=unknown
ARG TARGETOS
ARG TARGETARCH

RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
    -ldflags="-s -w \
      -X github.com/sartoopjj/thefeed/internal/version.Version=${VERSION} \
      -X github.com/sartoopjj/thefeed/internal/version.Commit=${COMMIT} \
      -X github.com/sartoopjj/thefeed/internal/version.Date=${DATE}" \
    -o /thefeed-server ./cmd/server

# ---- Stage 2: Runtime ----
FROM alpine:3.21

# Copy ca-certificates and tzdata from builder (avoids second apk fetch
# which can fail on restricted networks).
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /usr/share/zoneinfo /usr/share/zoneinfo

# Non-root user for security
RUN adduser -D -u 1000 -h /data thefeed

COPY --from=builder /thefeed-server /usr/local/bin/thefeed-server

# Data directory: channels.txt, x_accounts.txt, session.json, cache
VOLUME /data

# DNS listen port (mapped to host:53 via docker-compose)
EXPOSE 5300/udp

USER thefeed
WORKDIR /data

ENTRYPOINT ["thefeed-server"]
# No settings flags: they'd beat thefeed.env, and the admin panel's changes
# would be ignored. THEFEED_LISTEN=:5300 goes in /data/thefeed.env.
CMD ["--data-dir", "/data"]
