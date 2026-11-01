# ytprobe: the Phase 0 measurement kit (docs/phase0-runbook.md).
#
# Build:  docker build -t ytprobe .
# Run:    see docs/phase0-runbook.md (secrets are mounted read-only, never
#         baked into the image).

FROM golang:1.24.7-bookworm AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/ytprobe ./cmd/ytprobe

# Distroless: no shell, no package manager, CA certificates included. It runs
# as root so it can read the owner-only (chmod 600) secret files that are
# mounted read-only from the host.
FROM gcr.io/distroless/static-debian12:latest
COPY --from=build /out/ytprobe /usr/local/bin/ytprobe
ENV YT_PROXIES_FILE=/etc/securebrand-youtube/proxies.txt \
    YT_API_KEY_FILE=/etc/securebrand-youtube/youtube_api_key \
    YT_CHANNELS_FILE=/etc/securebrand-youtube/phase0-channels.txt \
    YT_DATA_DIR=/var/lib/securebrand-youtube/phase0
ENTRYPOINT ["/usr/local/bin/ytprobe"]
