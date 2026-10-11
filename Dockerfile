# Tier 1 image (scope §6): the server plus every helper tool (ffmpeg, yt-dlp, deno, rclone).
# Multi-arch: Go cross-compiles on the build machine; only the runtime stage runs per platform.
FROM --platform=$BUILDPLATFORM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG TARGETARCH TARGETVARIANT VERSION=dev
RUN CGO_ENABLED=0 GOARCH=$TARGETARCH GOARM=${TARGETVARIANT#v} go build -trimpath -buildvcs=false \
    -ldflags "-s -w -X main.version=${VERSION}" -o /dmn ./cmd/dmn

# Debian, not Alpine: the deno builds need glibc.
FROM debian:trixie-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates ffmpeg \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --system --uid 1000 --home-dir /data --shell /usr/sbin/nologin dmn \
    && mkdir -p /config /data /downloads /opt/dmn-seed && chown dmn /config /data /downloads
COPY --from=build /dmn /usr/local/bin/dmn
# Helper tools fetched by dmn's own verified installer (official releases, SHA-256 checked, self-test).
# deno has no armv7 build; video still works there without YouTube's JS challenges.
RUN for t in yt-dlp rclone deno; do dmn tools install --data /opt/dmn-seed "$t" || echo "skip $t on $(uname -m)"; done
COPY scripts/docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
USER dmn
ENV DMN_CONFIG=/config/config.yaml DMN_PATHS_DATA=/data DMN_PATHS_DOWNLOADS=/downloads DMN_SERVER_LISTEN=0.0.0.0:8080
VOLUME ["/config", "/data", "/downloads"]
EXPOSE 8080 42069 42069/udp
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s CMD ["dmn", "health"]
ENTRYPOINT ["sh", "/usr/local/bin/docker-entrypoint.sh"]
CMD ["serve"]
