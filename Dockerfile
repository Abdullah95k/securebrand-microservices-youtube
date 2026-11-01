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
# File locations default to /etc/securebrand-youtube/… and
# /var/lib/securebrand-youtube/phase0 inside the program (see cmd/ytprobe).
ENTRYPOINT ["/usr/local/bin/ytprobe"]
