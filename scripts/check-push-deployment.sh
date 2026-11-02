#!/usr/bin/env bash
# Check the optional gateway without starting containers or using credentials.
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
export LOOMPLANE_IMAGE=loomplane:test LOOMPLANE_DOMAIN=loomplane.example.com
export LOOMPLANE_SECRET_KEY=test POSTGRES_PASSWORD=test ACME_EMAIL=ops@example.com
export LOOMPLANE_PUSH_GATEWAY_URL=https://unused.example.com
export PUSH_GATEWAY_IMAGE=push-gateway:test PUSH_GATEWAY_DOMAIN=push.example.com
export PUSH_GATEWAY_SEALING_KEY_ID=test PUSH_GATEWAY_PREVIOUS_SEALING_KEY_ID=''
export PUSH_GATEWAY_APNS_KEY_ID='' PUSH_GATEWAY_APNS_TEAM_ID=''
export PUSH_GATEWAY_APNS_BUNDLE_ID='' PUSH_GATEWAY_SECRETS_DIR=./secrets
export COMPOSE_FILE='' COMPOSE_PROFILES=''
base=(docker compose --env-file /dev/null -f "$root/deployments/production/compose.yaml")
bundled=("${base[@]}" -f "$root/deployments/production/compose.push-gateway.yaml")
"${base[@]}" config --format json | jq -e '
  (.services | has("push-gateway") | not) and
  .services.loomplane.environment.LOOMPLANE_PUSH_GATEWAY_URL == "https://unused.example.com"
' >/dev/null
"${bundled[@]}" config --format json | jq -e '
  .services.loomplane.environment.LOOMPLANE_PUSH_GATEWAY_URL == "https://push.example.com" and
  .services["loomplane-worker"].environment.LOOMPLANE_PUSH_GATEWAY_URL == "https://push.example.com" and
  .services.caddy.depends_on["push-gateway"].condition == "service_healthy" and
  (.services.caddy.ports | length) == 2 and
  (.services["push-gateway"] | has("ports") | not) and
  (.services["push-gateway"].environment | has("LOOMPLANE_SECRET_KEY") | not) and
  .services["push-gateway"].environment.PUSH_GATEWAY_APNS_KEY_FILE == "" and
  (.services["push-gateway"].secrets | length) == 4 and
  .services["push-gateway"].read_only == true
' >/dev/null
for variable in PUSH_GATEWAY_IMAGE PUSH_GATEWAY_DOMAIN PUSH_GATEWAY_SEALING_KEY_ID; do
  if env "$variable=" "${bundled[@]}" config --quiet 2>/dev/null; then
    echo "The bundled gateway accepted an empty $variable" >&2
    exit 1
  fi
done
PUSH_GATEWAY_APNS_KEY_ID=ABCDE12345 PUSH_GATEWAY_APNS_TEAM_ID=TEAM123456 \
  "${bundled[@]}" config --format json | jq -e '
    .services["push-gateway"].environment.PUSH_GATEWAY_APNS_KEY_FILE == "/run/secrets/apns_key"
  ' >/dev/null
# Exercise the documented opt-in from an extracted release bundle too.
scratch=$(mktemp -d)
trap 'rm -rf "$scratch"' EXIT
cp "$root/deployments/production/compose.yaml" "$root/deployments/production/compose.push-gateway.yaml" \
  "$root/deployments/production/Caddyfile" "$root/deployments/production/Caddyfile.push-gateway" "$scratch/"
printf 'COMPOSE_FILE=compose.yaml:compose.push-gateway.yaml\n' >"$scratch/.env"
(
  cd "$scratch"
  env -u COMPOSE_FILE docker compose -p bundled-push-config config --format json
) | jq -e '.services | has("push-gateway")' >/dev/null
echo "Optional gateway Compose configuration passed."
