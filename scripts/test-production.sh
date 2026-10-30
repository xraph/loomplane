#!/usr/bin/env bash
# Exercise the production stack with disposable credentials and loopback TLS.
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
scratch=$(mktemp -d)
project="loomplane-smoke-$(date +%s)-$$"
export LOOMPLANE_IMAGE=${1:?Usage: scripts/test-production.sh IMAGE}
export LOOMPLANE_DOMAIN=localhost ACME_EMAIL=smoke@example.com
export LOOMPLANE_SECRET_KEY
LOOMPLANE_SECRET_KEY=$(openssl rand -base64 32)
export POSTGRES_PASSWORD
POSTGRES_PASSWORD=$(openssl rand -hex 32)
# Never inherit real service credentials in the disposable stack.
export LOOMPLANE_GITHUB_APP_ID='' LOOMPLANE_GITHUB_APP_SLUG=''
export LOOMPLANE_GITHUB_PRIVATE_KEY='' LOOMPLANE_GITHUB_CLIENT_ID=''
export LOOMPLANE_GITHUB_CLIENT_SECRET='' LOOMPLANE_GITHUB_WEBHOOK_SECRET=''
export LOOMPLANE_NOTIFICATIONS_SMTP_HOST='' LOOMPLANE_NOTIFICATIONS_SMTP_USERNAME=''
export LOOMPLANE_NOTIFICATIONS_SMTP_PASSWORD='' LOOMPLANE_NOTIFICATIONS_FROM=''
export LOOMPLANE_BACKUP_URL=''
export LOOMPLANE_PAUSED_PROVIDERS=''
export LOOMPLANE_GITHUB_API_URL=https://api.github.com LOOMPLANE_GITHUB_WEB_URL=https://github.com

cat >"$scratch/override.yaml" <<'YAML'
services:
  caddy:
    ports: !override
      - "127.0.0.1::443"
YAML
compose=(docker compose --env-file /dev/null -p "$project"
  -f "$root/deployments/production/compose.yaml" -f "$scratch/override.yaml")
cleanup() {
  result=$?
  if [ "$result" -ne 0 ]; then
    "${compose[@]}" ps || true
    "${compose[@]}" logs --tail=100 || true
  fi
  if ! "${compose[@]}" down --volumes --remove-orphans; then
    echo "Cleanup failed for Compose project $project" >&2
    result=1
  fi
  rm -rf "$scratch"
  exit "$result"
}
trap cleanup EXIT

# Empty settings must fail before starting a container.
if LOOMPLANE_SECRET_KEY='' "${compose[@]}" config --quiet 2>/dev/null; then
  echo "Production accepted an empty secret key" >&2
  exit 1
fi
if POSTGRES_PASSWORD='' "${compose[@]}" config --quiet 2>/dev/null; then
  echo "Production accepted an empty database password" >&2
  exit 1
fi
"${compose[@]}" config --quiet
"${compose[@]}" up -d --wait --wait-timeout 240
endpoint=$("${compose[@]}" port caddy 443)
port=${endpoint##*:}
url="https://localhost:$port"
# Only this isolated test accepts Caddy's local CA. Production uses public TLS.
curl --insecure --fail --silent --show-error --retry 15 --retry-connrefused \
  --retry-delay 1 "$url/" >"$scratch/index.html"
grep -q '<html' "$scratch/index.html"
test "$(curl --insecure --silent --output /dev/null --write-out '%{http_code}' "$url/_/health")" = 404
test "$(curl --insecure --silent --output /dev/null --write-out '%{http_code}' "$url/api/v1/projects")" = 401
# shellcheck disable=SC2016 # Expanded by the shell inside the container.
"${compose[@]}" exec -T loomplane sh -ec \
  'test "$(id -u)" = 10001; test "$LOOMPLANE_ENVIRONMENT" = production; test ! -w /etc/loomplane; echo persistent > /var/lib/loomplane/smoke-marker'
# The worker unpacks checkouts on a disk volume, not the 1 GiB RAM-backed /tmp.
# shellcheck disable=SC2016 # Expanded by the shell inside the container.
"${compose[@]}" exec -T loomplane-worker sh -ec \
  'test "$LOOMPLANE_ANALYSIS_WORK_DIR" = /var/lib/loomplane-work; dd if=/dev/zero of=/var/lib/loomplane-work/probe bs=1M count=1100; rm /var/lib/loomplane-work/probe'
"${compose[@]}" exec -T postgres psql -U loomplane -d loomplane -v ON_ERROR_STOP=1 \
  <<'SQL'
CREATE TABLE release_smoke (value text);
INSERT INTO release_smoke VALUES ('persistent');
SQL
"${compose[@]}" exec -T loomplane loomplane backup --out /tmp/smoke-backup.tar.gz
"${compose[@]}" exec -T loomplane loomplane backup inspect /tmp/smoke-backup.tar.gz

# Recreate containers to catch state accidentally kept in the writable layer.
"${compose[@]}" stop loomplane-worker loomplane
"${compose[@]}" up -d --force-recreate --wait --wait-timeout 240 postgres loomplane loomplane-worker
"${compose[@]}" exec -T loomplane grep -q persistent /var/lib/loomplane/smoke-marker
test "$("${compose[@]}" exec -T postgres psql -U loomplane -d loomplane -Atc 'SELECT value FROM release_smoke')" = persistent
"${compose[@]}" exec -T loomplane wget -qO /dev/null http://127.0.0.1:8780/_/health
"${compose[@]}" exec -T loomplane-worker wget -qO /dev/null http://127.0.0.1:8781/_/health
curl --insecure --fail --silent --show-error --retry 5 --retry-delay 1 "$url/" >"$scratch/restarted.html"
grep -q '<html' "$scratch/restarted.html"

# The setup token is derived from the secret key and survives the restart.
# Setup runs through the real endpoint with it, and the token is then gone.
setup_token=$("${compose[@]}" exec -T loomplane loomplane setup-token --raw | tr -d '\r\n')
if ! [[ "$setup_token" =~ ^[a-z2-7]{32}$ ]]; then
  echo "setup-token --raw didn't print a setup token (got: ${setup_token:0:40})" >&2
  exit 1
fi
setup_body() {
  printf '{"name":"Smoke Admin","email":"admin@smoke.test","password":"Correct-Horse-Battery-9-Staple","organization_name":"Smoke","token":"%s"}' "$1"
}
setup_status() {
  curl --insecure --silent --output "$scratch/setup.json" --write-out '%{http_code}' \
    -X POST -H 'Content-Type: application/json' -d "$(setup_body "$1")" "$url/api/v1/setup"
}
test "$(setup_status aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa)" = 403
test "$(setup_status "$setup_token")" = 200 || { cat "$scratch/setup.json" >&2; exit 1; }
"${compose[@]}" exec -T loomplane loomplane setup-token | grep -q 'Setup is complete\.'
done_raw=$("${compose[@]}" exec -T loomplane loomplane setup-token --raw | tr -d '\r\n')
if [[ "$done_raw" =~ ^[a-z2-7]{32}$ ]]; then
  echo "setup-token --raw still prints a token after setup" >&2
  exit 1
fi
echo "Production smoke passed: HTTPS, dashboard, authentication, probes, backup, persistence and setup token."
