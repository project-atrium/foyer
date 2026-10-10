# syntax=docker/dockerfile:1
#
# Foyer, the household's own server: one binary, Jellyfin's ffmpeg beside it,
# and its data in a volume.
#
#   docker build -t foyer .
#
# With the GPU for the transcoding to come: compose.gpu.yaml beside
# docker-compose.yml (docs/deployment).

FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# PocketBase's SQLite is pure Go: nothing to link, nothing to install beside it.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /foyer .

# Debian, for Jellyfin's own ffmpeg: built for VAAPI and Quick Sync, with the
# drivers they need inside the package, and the build a household's Jellyfin
# already transcodes with on the same hardware.
FROM debian:trixie-slim
# The key comes with curl, not ADD --chmod: that needs BuildKit, and a
# Docker without buildx builds with the old builder.
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates curl \
 && install -d -m 755 /etc/apt/keyrings \
 && curl -fsSL https://repo.jellyfin.org/jellyfin_team.gpg.key -o /etc/apt/keyrings/jellyfin.asc \
 && echo "deb [signed-by=/etc/apt/keyrings/jellyfin.asc] https://repo.jellyfin.org/debian trixie main" \
      > /etc/apt/sources.list.d/jellyfin.list \
 && apt-get update \
 && apt-get install -y --no-install-recommends jellyfin-ffmpeg8 \
 && rm -rf /var/lib/apt/lists/* \
 && ln -s /usr/lib/jellyfin-ffmpeg/ffmpeg /usr/local/bin/ffmpeg \
 && ln -s /usr/lib/jellyfin-ffmpeg/ffprobe /usr/local/bin/ffprobe \
 && useradd --system --uid 10001 --no-create-home --home-dir /nonexistent --shell /usr/sbin/nologin foyer \
 && mkdir /pb_data && chown foyer /pb_data
COPY --from=build /foyer /usr/local/bin/foyer
USER foyer
# Called by its name, the binary keeps pb_data in the working directory: here
# that is /pb_data, for `serve` and for `docker compose exec` alike.
WORKDIR /
VOLUME ["/pb_data"]
EXPOSE 8090
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD curl -fsS -o /dev/null http://127.0.0.1:8090/api/health || exit 1
CMD ["foyer", "serve", "--http=0.0.0.0:8090", "--dir=/pb_data"]
