# syntax=docker/dockerfile:1

# Loomplane in one image: the API, with the dashboard it serves.
#
#   docker build -f deployments/docker/Dockerfile -t loomplane .
#
# Build from the repository root. Every Go module comes from the public
# proxy, so the build needs no credentials.
#
# The image starts in development mode with the development secret key,
# which suits a laptop and nothing else. For anything shared, set
# LOOMPLANE_ENVIRONMENT=production, LOOMPLANE_SECRET_KEY and an https
# LOOMPLANE_PUBLIC_URL; production refuses to start without them.

ARG GO_VERSION=1.26
ARG NODE_VERSION=24
ARG ALPINE_VERSION=3.22

FROM node:${NODE_VERSION}-alpine AS dashboard
# The version package.json's packageManager names.
RUN npm install --global pnpm@11.5.0
WORKDIR /src/apps/dashboard
COPY apps/dashboard/package.json apps/dashboard/pnpm-lock.yaml apps/dashboard/pnpm-workspace.yaml ./
COPY apps/dashboard/patches ./patches
RUN --mount=type=cache,id=loomplane-pnpm,target=/pnpm/store \
    pnpm install --frozen-lockfile --store-dir /pnpm/store
COPY apps/dashboard ./
RUN pnpm build

FROM golang:${GO_VERSION}-alpine AS api
WORKDIR /src
ENV CGO_ENABLED=0
COPY go.mod go.sum ./
RUN --mount=type=cache,id=loomplane-gomod,target=/go/pkg/mod \
    go mod download
COPY cmd ./cmd
COPY database ./database
COPY extensions ./extensions
COPY internal ./internal
ARG VERSION=dev
RUN --mount=type=cache,id=loomplane-gomod,target=/go/pkg/mod \
    --mount=type=cache,id=loomplane-gobuild,target=/root/.cache/go-build \
    go build -trimpath \
      -ldflags "-s -w -X github.com/xraph/loomplane/internal/platform.Version=${VERSION}" \
      -o /out/loomplane ./cmd/loomplane

FROM alpine:${ALPINE_VERSION}
RUN apk add --no-cache ca-certificates tzdata \
 && adduser -D -H -u 10001 loomplane
COPY --from=api /out/loomplane /usr/local/bin/loomplane
COPY --from=dashboard /src/apps/dashboard/dist /usr/share/loomplane/dashboard
COPY config.yaml /etc/loomplane/config.yaml
ENV LOOMPLANE_CONFIG_DIR=/etc/loomplane \
    LOOMPLANE_DASHBOARD_DIR=/usr/share/loomplane/dashboard \
    LOOMPLANE_HTTP_ADDR=:8780
WORKDIR /etc/loomplane
USER loomplane
EXPOSE 8780
# The health check follows the default port. Change LOOMPLANE_HTTP_ADDR and
# you'll want to change this too.
HEALTHCHECK --interval=10s --timeout=3s --start-period=30s --retries=3 \
  CMD wget -qO /dev/null http://127.0.0.1:8780/_/health/live || exit 1
ENTRYPOINT ["loomplane"]
