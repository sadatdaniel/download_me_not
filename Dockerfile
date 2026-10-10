# M3 adds yt-dlp, deno, ffmpeg and rclone to the runtime image (tier 1 bundles every helper).
FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /dmn ./cmd/dmn

FROM alpine:3.22
RUN apk add --no-cache ca-certificates && adduser -D -u 1000 dmn && mkdir -p /config /data /downloads && chown dmn /config /data /downloads
COPY --from=build /dmn /usr/local/bin/dmn
USER dmn
ENV DMN_CONFIG=/config/config.yaml DMN_PATHS_DATA=/data DMN_PATHS_DOWNLOADS=/downloads DMN_SERVER_LISTEN=0.0.0.0:8080
VOLUME ["/config", "/data", "/downloads"]
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s CMD wget -qO- http://127.0.0.1:8080/v1/health >/dev/null || exit 1
ENTRYPOINT ["dmn"]
CMD ["serve"]
