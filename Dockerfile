# One image, two commands:
#   ytprobe    the Phase 0 measurement kit (docs/phase0-runbook.md), the default
#   ytcollect  the production collector (--entrypoint /usr/local/bin/ytcollect)
#
# Build:  docker build -t ytprobe .
#         (a release adds --build-arg VERSION=v0.1.0 --build-arg REVISION=<commit>)
# Run:    see docs/production-runbook.md and docs/phase0-runbook.md (secrets
#         are mounted read-only, never baked into the image).
#
# Both base images are pinned by digest (REQ-010 fix 16, standard I3), so a
# rebuild uses exactly the same bases. To move to newer ones, change the tag
# and the digest together; CI scans the result.

FROM golang:1.27.4-trixie@sha256:b54b2639de8c1bfe538ec5ffc45f29b1932235ac5f6d63c059853646677112d9 AS build
ARG VERSION=dev
ARG REVISION=unknown
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags="-s -w -X github.com/Abdullah95k/securebrand-microservices-youtube/internal/version.Version=${VERSION} -X github.com/Abdullah95k/securebrand-microservices-youtube/internal/version.Revision=${REVISION}" \
    -o /out/ ./cmd/ytprobe ./cmd/ytcollect

# Distroless "nonroot" (REQ-010 fix 13, standard A2): no shell, no package
# manager, CA certificates included, and it runs as user and group 65532,
# never root. It needs no capabilities and works with a read-only root
# filesystem: the programs write only under /var/lib/securebrand-youtube (a
# volume) and /tmp (an emptyDir or tmpfs).
#
# Secret files must be readable by 65532: owned by it (chmod 600) on a
# server, or through the pod's fsGroup on Kubernetes (mode 0440, see
# internal/secrets).
FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
ARG VERSION=dev
ARG REVISION=unknown
LABEL org.opencontainers.image.title="yts" \
      org.opencontainers.image.description="SecureBrand YouTube collector" \
      org.opencontainers.image.source="https://github.com/Abdullah95k/securebrand-microservices-youtube" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${REVISION}"
COPY --from=build /out/ytprobe /out/ytcollect /usr/local/bin/
USER 65532:65532
# File locations default to /etc/securebrand-youtube/… and
# /var/lib/securebrand-youtube/… inside the programs.
ENTRYPOINT ["/usr/local/bin/ytprobe"]
