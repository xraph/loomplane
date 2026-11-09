# syntax=docker/dockerfile:1

# The Loomplane push gateway: a small relay that seals device handles and
# forwards pushes to FCM and APNs. It holds no secrets. Keys arrive at run
# time as files named by PUSH_GATEWAY_*_FILE variables.
#
#   docker build -f deployments/push-gateway/Dockerfile -t loomplane-push-gateway .
#
# Build from the repository root.

ARG GO_VERSION=1.26.10
ARG ALPINE_VERSION=3.22

FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine AS build
WORKDIR /src
ENV CGO_ENABLED=0
COPY go.mod go.sum ./
RUN --mount=type=cache,id=loomplane-push-gateway-gomod,target=/go/pkg/mod \
    go mod download
COPY cmd/loomplane-push-gateway ./cmd/loomplane-push-gateway
COPY internal/buildinfo ./internal/buildinfo
ARG VERSION=dev
ARG TARGETOS
ARG TARGETARCH
RUN --mount=type=cache,id=loomplane-push-gateway-gomod,target=/go/pkg/mod \
    --mount=type=cache,id=loomplane-push-gateway-gobuild,target=/root/.cache/go-build \
    GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath \
      -ldflags "-s -w -X github.com/xraph/loomplane/internal/buildinfo.Version=${VERSION}" \
      -o /out/loomplane-push-gateway ./cmd/loomplane-push-gateway

FROM alpine:${ALPINE_VERSION}
RUN apk add --no-cache ca-certificates \
 && adduser -D -H -u 10001 pushgateway
COPY --from=build /out/loomplane-push-gateway /usr/local/bin/loomplane-push-gateway
USER 10001
EXPOSE 8790
HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=3 \
  CMD wget -qO /dev/null http://127.0.0.1:8790/_/health || exit 1
ENTRYPOINT ["loomplane-push-gateway"]
