# syntax=docker/dockerfile:1

# Loomplane's server proxy: Caddy 2.11.7 with the Cloudflare DNS module, so
# a server can answer DNS-01 challenges for wildcard routes. Everything
# else is the stock image Loomplane ran before, file for file.
#
#   docker build -f deployments/ingress/Dockerfile -t loomplane-ingress deployments/ingress
#
# Both images are pinned by their multi-platform digest and the module by
# its release tag. .github/workflows/publish-ingress.yml builds and
# publishes it; internal/clouds/server pins what it publishes.

FROM caddy:2.11.7-builder-alpine@sha256:bb53bc0dd94d5e93eca88bac50e435fd5f9dc6cc828fc6140086f282fb329e92 AS build
RUN xcaddy build v2.11.7 --with github.com/caddy-dns/cloudflare@v0.2.4

FROM caddy:2.11.7-alpine@sha256:d8542f48d34a9cf4e4c11a478865229840e87e4c96ea3f439101f31a5d35f75f
COPY --from=build /usr/bin/caddy /usr/bin/caddy
RUN caddy list-modules | grep -q dns.providers.cloudflare
