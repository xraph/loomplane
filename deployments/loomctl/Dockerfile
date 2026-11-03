# The loomctl image, ghcr.io/xraph/loomctl. GoReleaser builds it, from the
# loomctl binary it has just built for the platform, so there is no build
# stage here. A machine without a service manager joins as a container:
#
#   docker run -d --restart unless-stopped \
#     -v loomplane-node:/var/lib/loomplane-node \
#     ghcr.io/xraph/loomctl node run
#
# Join the machine first (node join --foreground, or with a token) so the
# volume holds its identity. .goreleaser.yaml (dockers) builds this file.
#
# The container runs as root on purpose. A node that offers Docker needs the
# host's Docker socket, which is mounted root-owned, and a non-root user with
# no group on that socket could not use it. Mount only what the node needs.
#
# The base is pinned by digest (alpine 3.22, a multi-platform index), so a
# moved tag cannot change the image. GoReleaser (dockers_v2) puts the binary
# for each platform in $TARGETPLATFORM/ in the build context.
FROM alpine:3.22@sha256:2e0b81d60597f449baa56dc25d355367869d2dca4cd57e866f1e2b52af3eb55f
ARG TARGETPLATFORM
RUN apk add --no-cache ca-certificates
COPY ${TARGETPLATFORM}/loomctl /usr/local/bin/loomctl
ENTRYPOINT ["loomctl"]
