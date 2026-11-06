# One image, two commands:
#   ytprobe    the Phase 0 measurement kit (docs/phase0-runbook.md), the default
#   ytcollect  the production collector (--entrypoint /usr/local/bin/ytcollect)
#
# Build:  docker build -t ytprobe .
# Run:    see docs/production-runbook.md and docs/phase0-runbook.md (secrets
#         are mounted read-only, never baked into the image).

FROM golang:1.27.3-trixie AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/ ./cmd/ytprobe ./cmd/ytcollect

# Distroless "nonroot" (REQ-010 fix 13, standard A2): no shell, no package
# manager, CA certificates included, and it runs as user and group 65532,
# never root. It needs no capabilities and works with a read-only root
# filesystem: the programs write only under /var/lib/securebrand-youtube (a
# volume) and /tmp (an emptyDir or tmpfs).
#
# Secret files must be readable by 65532: owned by it (chmod 600) on a
# server, or through the pod's fsGroup on Kubernetes (mode 0440, see
# internal/secrets).
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/ytprobe /out/ytcollect /usr/local/bin/
USER 65532:65532
# File locations default to /etc/securebrand-youtube/… and
# /var/lib/securebrand-youtube/… inside the programs.
ENTRYPOINT ["/usr/local/bin/ytprobe"]
