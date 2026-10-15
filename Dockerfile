# Tier 1 image (scope §6): the server plus every helper tool (ffmpeg, yt-dlp, deno, rclone).
# Base images are pinned by digest (Dependabot proposes updates).
# Multi-arch: Go cross-compiles on the build machine; only the runtime stage runs per platform.
FROM --platform=$BUILDPLATFORM golang:1.26@sha256:37cfcca63222ec499bec5e8ad7aad52dbf7529db36d5812b8eae2f0621aa92d6 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG TARGETARCH TARGETVARIANT VERSION=dev
RUN CGO_ENABLED=0 GOARCH=$TARGETARCH GOARM=${TARGETVARIANT#v} go build -trimpath -buildvcs=false \
    -ldflags "-s -w -X main.version=${VERSION}" -o /getaload ./cmd/getaload

# Debian, not Alpine: the deno builds need glibc.
FROM debian:trixie-slim@sha256:a29215f6a35e51e22adffa17f89e9d2ef06214e64a2bad10d765c46aea49f11f
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates ffmpeg \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --system --uid 1000 --home-dir /data --shell /usr/sbin/nologin getaload \
    && mkdir -p /config /data /downloads /opt/getaload-seed && chown getaload /config /data /downloads
COPY --from=build /getaload /usr/local/bin/getaload
# Helper tools fetched by getaload's own verified installer (official releases, SHA-256 checked, self-test).
# deno has no armv7 build; video still works there without YouTube's JS challenges.
RUN for t in yt-dlp rclone deno; do getaload tools install --data /opt/getaload-seed "$t" || echo "skip $t on $(uname -m)"; done
COPY scripts/docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
USER getaload
ENV GETALOAD_IMAGE=1 GETALOAD_CONFIG=/config/config.yaml GETALOAD_PATHS_DATA=/data GETALOAD_PATHS_DOWNLOADS=/downloads GETALOAD_SERVER_LISTEN=0.0.0.0:5623
VOLUME ["/config", "/data", "/downloads"]
EXPOSE 5623 42069 42069/udp
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s CMD ["getaload", "health"]
ENTRYPOINT ["sh", "/usr/local/bin/docker-entrypoint.sh"]
CMD ["serve"]
