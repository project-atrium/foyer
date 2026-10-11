# syntax=docker/dockerfile:1
#
# Foyer, the household's own server: one binary, Jellyfin's ffmpeg, yt-dlp
# and Deno beside it, and its data in a volume.
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
# yt-dlp, which says where a YouTube video's streams are, and Deno, the
# JavaScript runtime it solves the site's challenges with: their own
# standalone builds, pinned, and checked against the sums they publish. The
# site changes often: raise YTDLP_VERSION and build again.
ARG YTDLP_VERSION=2026.08.19
ARG DENO_VERSION=v2.9.7
RUN apt-get update \
 && apt-get install -y --no-install-recommends unzip \
 && case "$(dpkg --print-architecture)" in \
      amd64) yt=yt-dlp_linux; deno=deno-x86_64-unknown-linux-gnu.zip ;; \
      arm64) yt=yt-dlp_linux_aarch64; deno=deno-aarch64-unknown-linux-gnu.zip ;; \
      *) echo "no yt-dlp or Deno for $(dpkg --print-architecture)" >&2; exit 1 ;; \
    esac \
 && cd /tmp \
 && curl -fsSLO "https://github.com/yt-dlp/yt-dlp/releases/download/${YTDLP_VERSION}/${yt}" \
 && curl -fsSL "https://github.com/yt-dlp/yt-dlp/releases/download/${YTDLP_VERSION}/SHA2-256SUMS" | grep " ${yt}\$" | sha256sum -c - \
 && install -m 755 "${yt}" /usr/local/bin/yt-dlp \
 && curl -fsSLO "https://github.com/denoland/deno/releases/download/${DENO_VERSION}/${deno}" \
 && curl -fsSL "https://github.com/denoland/deno/releases/download/${DENO_VERSION}/${deno}.sha256sum" | sha256sum -c - \
 && unzip -q "${deno}" -d /usr/local/bin \
 && rm -f "${yt}" "${deno}" \
 && apt-get purge -y unzip && apt-get autoremove -y && rm -rf /var/lib/apt/lists/*
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
