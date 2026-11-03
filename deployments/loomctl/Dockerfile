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
ARG ALPINE_VERSION=3.22
FROM alpine:${ALPINE_VERSION}
RUN apk add --no-cache ca-certificates
COPY loomctl /usr/local/bin/loomctl
ENTRYPOINT ["loomctl"]
