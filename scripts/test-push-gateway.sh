#!/usr/bin/env bash
# Exercise the push gateway stack with throwaway secrets and loopback TLS.
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
scratch=$(mktemp -d)
project="loomplane-push-smoke-$(date +%s)-$$"
export PUSH_GATEWAY_IMAGE=${1:?Usage: scripts/test-push-gateway.sh IMAGE}
export PUSH_GATEWAY_DOMAIN=localhost ACME_EMAIL=smoke@example.com
export PUSH_GATEWAY_SECRETS_DIR=$scratch
export PUSH_GATEWAY_SEALING_KEY_ID=smoke
export PUSH_GATEWAY_APNS_KEY_ID=ABCDE12345 PUSH_GATEWAY_APNS_TEAM_ID=TEAM123456
# Never inherit real settings in the disposable stack.
export PUSH_GATEWAY_PREVIOUS_SEALING_KEY_ID=''
export PUSH_GATEWAY_SERVER_RATE='' PUSH_GATEWAY_HANDLE_RATE='' CADDY_IMAGE=''

# Throwaway secrets. Bind-mounted secrets keep host ownership, so they are
# world-readable for the container's user; none of these is real.
openssl rand -base64 32 >"$scratch/sealing-key"
: >"$scratch/previous-sealing-key"
openssl genpkey -algorithm EC -pkeyopt ec_paramgen_curve:P-256 -out "$scratch/apns-key.p8" 2>/dev/null
openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:2048 -out "$scratch/rsa.pem" 2>/dev/null
jq -n --rawfile key "$scratch/rsa.pem" \
  '{type: "service_account", project_id: "smoke", private_key_id: "smoke",
    private_key: $key, client_email: "smoke@smoke.iam.gserviceaccount.com",
    token_uri: "https://oauth2.googleapis.com/token"}' >"$scratch/fcm-service-account.json"
rm "$scratch/rsa.pem"
chmod 0444 "$scratch"/sealing-key "$scratch"/previous-sealing-key \
  "$scratch"/apns-key.p8 "$scratch"/fcm-service-account.json

cat >"$scratch/override.yaml" <<'YAML'
services:
  caddy:
    ports: !override
      - "127.0.0.1::443"
YAML
compose=(docker compose --env-file /dev/null -p "$project"
  -f "$root/deployments/push-gateway/compose.yaml" -f "$scratch/override.yaml")
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

# Missing settings must fail before starting a container.
if PUSH_GATEWAY_SEALING_KEY_ID='' "${compose[@]}" config --quiet 2>/dev/null; then
  echo "Compose accepted an empty sealing key ID" >&2
  exit 1
fi
if PUSH_GATEWAY_IMAGE='' "${compose[@]}" config --quiet 2>/dev/null; then
  echo "Compose accepted an empty image" >&2
  exit 1
fi
# Turning iOS off means unsetting the key ID; the key file goes with it.
off=$(PUSH_GATEWAY_APNS_KEY_ID='' PUSH_GATEWAY_APNS_TEAM_ID='' "${compose[@]}" config)
if grep -Eq 'PUSH_GATEWAY_APNS_KEY_FILE: [^"]' <<<"$off"; then
  echo "APNs key file still set with iOS off" >&2
  exit 1
fi
"${compose[@]}" config --quiet
"${compose[@]}" up -d --wait --wait-timeout 120
endpoint=$("${compose[@]}" port caddy 443)
port=${endpoint##*:}
url="https://localhost:$port"
# Only this isolated test accepts Caddy's local CA. Production uses public TLS.
code() { curl --insecure --silent --output /dev/null --write-out '%{http_code}' "$@"; }
for _ in $(seq 1 20); do
  [ "$(code "$url/")" = 404 ] && break
  sleep 1
done

test "$(code "$url/_/health")" = 404
test "$(code "$url/")" = 404
test "$(code -X POST -H 'Content-Type: application/json' -d '{}' "$url/v1/registrations")" = 400
server_key=$(head -c 32 /dev/urandom | base64)
registration=$(curl --insecure --silent --show-error --write-out '\n%{http_code}' \
  -X POST -H 'Content-Type: application/json' \
  -d "{\"platform\":\"android\",\"token\":\"smoke-token\",\"server_key\":\"$server_key\"}" \
  "$url/v1/registrations")
test "$(tail -n 1 <<<"$registration")" = 201
case "$(head -n 1 <<<"$registration" | jq -r .handle)" in
  h1.smoke.*) ;;
  *) echo "Unexpected handle: sealing key ID missing" >&2; exit 1 ;;
esac
head -c 71680 /dev/zero | tr '\0' 'a' >"$scratch/big.txt"
test "$(code -X POST -H 'Content-Type: application/json' --data-binary "@$scratch/big.txt" "$url/v1/send")" = 413

# shellcheck disable=SC2016 # Expanded by the shell inside the container.
"${compose[@]}" exec -T push-gateway sh -ec \
  'test "$(id -u)" = 10001; if touch /x 2>/dev/null; then exit 1; fi'

logs=$("${compose[@]}" logs --no-color push-gateway)
if grep -Fq smoke-token <<<"$logs" || grep -Fq 'endpoint override in use' <<<"$logs"; then
  echo "Gateway logs hold a token or an endpoint override" >&2
  exit 1
fi

"${compose[@]}" stop push-gateway
grep -Fq 'push gateway stopped' <<<"$("${compose[@]}" logs --no-color push-gateway)"
echo "Push gateway smoke passed: HTTPS, routes, limits, user, secrets and shutdown."
