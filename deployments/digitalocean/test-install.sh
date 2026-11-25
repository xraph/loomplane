#!/usr/bin/env bash
# Tests for install.sh. Run from anywhere: `bash deployments/digitalocean/test-install.sh`.
#
# On a workstation this re-runs itself inside an ubuntu:24.04 container with
# the repo mounted read-only, serves a fixture release bundle from a local
# HTTP server, and drives install.sh through its test hooks. When Docker is
# available on the host, it finishes by running `docker compose config
# --quiet` against the install the container produced.
set -euo pipefail

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
repo=$(cd "$here/../.." && pwd)

if [ -z "${LOOMPLANE_TEST_IN_CONTAINER:-}" ]; then
  command -v docker >/dev/null || { echo "test-install: docker is required" >&2; exit 1; }
  out=$(mktemp -d)
  trap 'rm -rf "$out"' EXIT
  docker run --rm -e LOOMPLANE_TEST_IN_CONTAINER=1 \
    -e LOOMPLANE_TEST_HOST_UID="$(id -u)" -e LOOMPLANE_TEST_HOST_GID="$(id -g)" \
    -v "$repo:/repo:ro" -v "$out:/out" ubuntu:24.04 bash /repo/deployments/digitalocean/test-install.sh
  echo "test-install: docker compose config on the installed bundle"
  (
    cd "$out/install"
    env -u COMPOSE_FILE -u COMPOSE_PROFILES -u PUSH_GATEWAY_DOMAIN docker compose config --format json | jq -e '
      .services.loomplane.environment.LOOMPLANE_PUSH_GATEWAY_URL == "https://push.loomplane.test.invalid" and
      .services["loomplane-worker"].environment.LOOMPLANE_PUSH_GATEWAY_URL == "https://push.loomplane.test.invalid" and
      .services.caddy.depends_on["push-gateway"].condition == "service_healthy"
    ' >/dev/null
  )
  echo "test-install: docker compose config on the install push-gateway turned on"
  (
    cd "$out/gw"
    env -u COMPOSE_FILE -u COMPOSE_PROFILES -u PUSH_GATEWAY_DOMAIN docker compose config --format json | jq -e '
      .services["push-gateway"].environment.PUSH_GATEWAY_APNS_KEY_ID == "ABCDE12345" and
      .services["push-gateway"].environment.PUSH_GATEWAY_APNS_KEY_FILE == "/run/secrets/apns_key" and
      .services.caddy.environment.PUSH_GATEWAY_DOMAIN == "push.loomplane.test.invalid" and
      .services.loomplane.environment.LOOMPLANE_PUSH_GATEWAY_URL == "https://push.loomplane.test.invalid"
    ' >/dev/null
  )
  echo "test-install: PASS (host compose config ok)"
  if [ "$#" -eq 2 ]; then
    echo "test-install: smoke test of the installed gateway bundle"
    bash "$repo/scripts/test-push-gateway.sh" "$2" "$1" "$out/install"
  elif [ "$#" -ne 0 ]; then
    echo "Usage: test-install.sh [LOOMPLANE_IMAGE GATEWAY_IMAGE]" >&2
    exit 2
  fi
  exit 0
fi

# ---- inside the container ----
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq >/dev/null
apt-get install -y -qq curl python3 util-linux openssl ca-certificates >/dev/null

installer=/repo/deployments/digitalocean/install.sh
work=$(mktemp -d)
srv=$work/srv
fail=0

pass() { echo "ok   $1"; }
bad() { echo "FAIL $1" >&2; fail=1; }

# Build the fixture the way publish-docker.yml builds the release bundle.
build_bundle() {
  local tag=$1 digest=${2:-sha256:0000} dir=$srv/$1 bundle=$work/bundle-$1
  mkdir -p "$dir" "$bundle"
  cp /repo/deployments/production/compose.yaml /repo/deployments/production/Caddyfile \
    /repo/deployments/production/.env.example /repo/deployments/production/compose.push-gateway.yaml \
    /repo/deployments/production/Caddyfile.push-gateway "$bundle/"
  cp /repo/docs/docker-production.md "$bundle/README.md"
  echo "ghcr.io/xraph/loomplane@$digest" >"$bundle/image.txt"
  sed -i "s|^LOOMPLANE_IMAGE=.*|LOOMPLANE_IMAGE=ghcr.io/xraph/loomplane@$digest|" "$bundle/.env.example"
  tar --owner=1001 --group=1001 --mode=g+w -czf "$dir/loomplane-$tag-docker.tar.gz" -C "$bundle" .
  (cd "$dir" && sha256sum "loomplane-$tag-docker.tar.gz" >SHA256SUMS)
}
build_bundle v9.9.9
build_bundle v9.9.8
# Tamper with v9.9.8 after its checksum was recorded.
printf 'x' >>"$srv/v9.9.8/loomplane-v9.9.8-docker.tar.gz"

(cd "$srv" && python3 -m http.server 8099 --bind 127.0.0.1 >/dev/null 2>&1) &
server=$!
trap 'kill "$server" 2>/dev/null || true' EXIT
for _ in $(seq 1 50); do curl -fs -o /dev/null http://127.0.0.1:8099/ && break; sleep 0.1; done

export LOOMPLANE_RELEASE_BASE_URL=http://127.0.0.1:8099
export LOOMPLANE_SKIP_DOCKER=1
install_dir=$work/install
export LOOMPLANE_DIR=$install_dir

# 1. prepare downloads, verifies and extracts the pinned bundle.
if LOOMPLANE_VERSION=v9.9.9 bash "$installer" prepare >"$work/prepare.log" 2>&1 \
  && [ -f "$install_dir/compose.yaml" ] && [ -f "$install_dir/Caddyfile" ] \
  && grep -q '^LOOMPLANE_IMAGE=ghcr.io/xraph/loomplane@sha256:0000$' "$install_dir/.env.example"; then
  pass "prepare extracts the bundle"
else
  bad "prepare extracts the bundle"; cat "$work/prepare.log" >&2
fi

# 1b. The install is root-owned and not writable by group or others, even
# though the fixture tarball was built as uid 1001 with group-writable files.
bad_perm=$(find "$install_dir" \( ! -user root -o ! -group root -o -perm /022 \) | head -n 3)
if [ -z "$bad_perm" ] && [ "$(stat -c %U "$install_dir/compose.yaml")" = root ]; then
  pass "install is root-owned and not group or world writable"
else
  bad "install is root-owned and not group or world writable: $bad_perm"
fi

# 2. secrets is idempotent and keeps .env private.
bash "$installer" secrets >/dev/null 2>&1 || bad "secrets first run"
key1=$(grep '^LOOMPLANE_SECRET_KEY=' "$install_dir/.env")
pw1=$(grep '^POSTGRES_PASSWORD=' "$install_dir/.env")
bash "$installer" secrets >/dev/null 2>&1 || bad "secrets second run"
key2=$(grep '^LOOMPLANE_SECRET_KEY=' "$install_dir/.env")
pw2=$(grep '^POSTGRES_PASSWORD=' "$install_dir/.env")
if [ "$key1" = "$key2" ] && [ "$pw1" = "$pw2" ] && [ "$key1" != "LOOMPLANE_SECRET_KEY=" ] \
  && [[ "$pw1" =~ ^POSTGRES_PASSWORD=[0-9a-f]{64}$ ]]; then
  pass "secrets are generated once"
else
  bad "secrets are generated once"
fi
if [ "$(stat -c %a "$install_dir/.env")" = "600" ]; then pass ".env is mode 600"; else bad ".env is mode 600"; fi

# 3. configure rejects anything but a bare hostname.
for bad_domain in "https://x.loomplane.test.invalid/" "x.loomplane.test.invalid/path"; do
  if msg=$(LOOMPLANE_DOMAIN=$bad_domain ACME_EMAIL=ops@loomplane.test.invalid bash "$installer" configure 2>&1); then
    bad "configure rejects $bad_domain"
  elif [[ "$msg" == *"bare hostname"* ]]; then
    pass "configure rejects $bad_domain"
  else
    bad "configure rejects $bad_domain (message: $msg)"
  fi
done

# 4. configure writes the domain, email and concurrency.
if LOOMPLANE_DOMAIN=x.loomplane.test.invalid ACME_EMAIL=ops@loomplane.test.invalid LOOMPLANE_ANALYSIS_CONCURRENCY=3 \
  bash "$installer" configure >/dev/null 2>&1 \
  && grep -q '^LOOMPLANE_DOMAIN=x.loomplane.test.invalid$' "$install_dir/.env" \
  && grep -q '^ACME_EMAIL=ops@loomplane.test.invalid$' "$install_dir/.env" \
  && grep -q '^LOOMPLANE_ANALYSIS_CONCURRENCY=3$' "$install_dir/.env" \
  && [ "$(grep -c '^LOOMPLANE_SECRET_KEY=' "$install_dir/.env")" = 1 ] \
  && [ "$(grep '^LOOMPLANE_SECRET_KEY=' "$install_dir/.env")" = "$key1" ]; then
  pass "configure writes .env"
else
  bad "configure writes .env"
fi

# 4b. configure rejects unedited cloud-init placeholders and bad concurrency.
for pair in "loomplane.example.com|ops@loomplane.test.invalid|placeholder" \
  "example.com|ops@loomplane.test.invalid|placeholder" \
  "x.loomplane.test.invalid|ops@example.com|placeholder" \
  "x.loomplane.test.invalid|ops@loomplane.test.invalid|at least 1"; do
  IFS='|' read -r d e want <<<"$pair"
  c=""; [ "$want" = "at least 1" ] && c=0
  if msg=$(LOOMPLANE_DOMAIN=$d ACME_EMAIL=$e LOOMPLANE_ANALYSIS_CONCURRENCY=$c bash "$installer" configure 2>&1); then
    bad "configure rejects $d / $e / concurrency '$c'"
  elif [[ "$msg" == *"$want"* ]]; then
    pass "configure rejects $d / $e / concurrency '$c'"
  else
    bad "configure rejects $d / $e / concurrency '$c' (message: $msg)"
  fi
done

# 4c. all validates before it touches the system.
allfresh=$work/all-fresh
if msg=$(LOOMPLANE_DIR=$allfresh LOOMPLANE_VERSION=v9.9.9 LOOMPLANE_DOMAIN=loomplane.example.com \
  ACME_EMAIL=ops@loomplane.test.invalid bash "$installer" all 2>&1); then
  bad "all rejects placeholders"
elif [[ "$msg" == *"placeholder"* ]] && [ ! -e "$allfresh" ]; then
  pass "all fails on placeholders before creating the install dir"
else
  bad "all fails on placeholders before creating the install dir (message: $msg)"
fi

# 4d. Upgrading prepare syncs LOOMPLANE_IMAGE and keeps secrets and config.
mkdir -m 0700 "$install_dir/secrets"
printf 'fixture-gateway-key' >"$install_dir/secrets/sealing-key"
chown 10001:10001 "$install_dir/secrets/sealing-key"
chmod 0400 "$install_dir/secrets/sealing-key"
mkdir -m 0700 "$install_dir/gateway-keys"
printf 'fixture-custom-key' >"$install_dir/gateway-keys/sealing-key"
chown 10001:10001 "$install_dir/gateway-keys/sealing-key"
chmod 0400 "$install_dir/gateway-keys/sealing-key"
printf '\nPUSH_GATEWAY_SECRETS_DIR=./gateway-keys\n' >>"$install_dir/.env"
printf 'COMPOSE_FILE=compose.yaml:compose.push-gateway.yaml\nPUSH_GATEWAY_DOMAIN=push.loomplane.test.invalid\n' >>"$install_dir/.env"
build_bundle v9.9.7 sha256:1111
if LOOMPLANE_VERSION=v9.9.7 bash "$installer" prepare >"$work/upgrade.log" 2>&1 \
  && grep -q '^LOOMPLANE_IMAGE=ghcr.io/xraph/loomplane@sha256:1111$' "$install_dir/.env" \
  && [ "$(grep '^LOOMPLANE_SECRET_KEY=' "$install_dir/.env")" = "$key1" ] \
  && [ "$(grep '^POSTGRES_PASSWORD=' "$install_dir/.env")" = "$pw1" ] \
  && grep -q '^LOOMPLANE_DOMAIN=x.loomplane.test.invalid$' "$install_dir/.env" \
  && grep -q '^COMPOSE_FILE=compose.yaml:compose.push-gateway.yaml$' "$install_dir/.env" \
  && grep -q '^PUSH_GATEWAY_DOMAIN=push.loomplane.test.invalid$' "$install_dir/.env" \
  && [ "$(stat -c %a "$install_dir/.env")" = 600 ] \
  && [ -z "$(find "$install_dir" -maxdepth 1 -name '.env.??????' | head -n 1)" ]; then
  pass "prepare on an upgrade syncs LOOMPLANE_IMAGE and keeps secrets"
else
  bad "prepare on an upgrade syncs LOOMPLANE_IMAGE and keeps secrets"; cat "$work/upgrade.log" >&2
fi

if [ "$(stat -c '%u:%g:%a' "$install_dir/secrets/sealing-key")" = 10001:10001:400 ] \
  && [ "$(stat -c %a "$install_dir/secrets")" = 700 ] \
  && [ "$(cat "$install_dir/secrets/sealing-key")" = fixture-gateway-key ] \
  && [ "$(stat -c '%u:%g:%a' "$install_dir/gateway-keys/sealing-key")" = 10001:10001:400 ] \
  && [ "$(stat -c %a "$install_dir/gateway-keys")" = 700 ] \
  && [ "$(cat "$install_dir/gateway-keys/sealing-key")" = fixture-custom-key ]; then
  pass "prepare preserves gateway key ownership, mode and contents"
else
  bad "prepare preserves gateway key ownership, mode and contents"
fi

# Keep the finished install for the host's `docker compose config` check.
rm -rf /out/install
cp -R --no-preserve=ownership "$install_dir" /out/install
# The preservation checks above run against the root-owned install. Hand
# only this disposable copy to the host so Linux CI can read .env and
# remove the mode-0700 fixture directories after the test.
chown -R "${LOOMPLANE_TEST_HOST_UID:-0}:${LOOMPLANE_TEST_HOST_GID:-0}" /out/install
if setpriv --reuid="${LOOMPLANE_TEST_HOST_UID:-0}" --regid="${LOOMPLANE_TEST_HOST_GID:-0}" --clear-groups \
  sh -c 'test -r /out/install/.env && test -r /out/install/gateway-keys/sealing-key'; then
  pass "exported bundle is readable by the Linux host user"
else
  bad "exported bundle is readable by the Linux host user"
fi

# 4e. push-gateway, on an install of its own.
gw=$work/gw
gwlog=$work/gw.log
LOOMPLANE_DIR=$gw LOOMPLANE_VERSION=v9.9.9 bash "$installer" prepare >/dev/null 2>&1 || bad "gateway install prepare"
LOOMPLANE_DIR=$gw bash "$installer" secrets >/dev/null 2>&1 || bad "gateway install secrets"
LOOMPLANE_DIR=$gw LOOMPLANE_DOMAIN=x.loomplane.test.invalid ACME_EMAIL=ops@loomplane.test.invalid \
  bash "$installer" configure >/dev/null 2>&1 || bad "gateway install configure"
fcm=$work/fcm.json
printf '{"type":"service_account","project_id":"p","private_key":"k","client_email":"e@p.iam"}\n' >"$fcm"
apns=$work/apns.p8
printf -- '-----BEGIN PRIVATE KEY-----\nk\n-----END PRIVATE KEY-----\n' >"$apns"
notkey=$work/notkey.json
printf '{}\n' >"$notkey"
gateway() { env LOOMPLANE_DIR="$gw" "$@" bash "$installer" push-gateway; }

for case in "|PUSH_GATEWAY_DOMAIN" \
  "PUSH_GATEWAY_DOMAIN=push.example.com|placeholder" \
  "PUSH_GATEWAY_DOMAIN=https://push.loomplane.test.invalid|bare hostname" \
  "PUSH_GATEWAY_DOMAIN=x.loomplane.test.invalid|of its own" \
  "PUSH_GATEWAY_DOMAIN=push.loomplane.test.invalid PUSH_GATEWAY_FCM_FILE=$notkey|service-account" \
  "PUSH_GATEWAY_DOMAIN=push.loomplane.test.invalid PUSH_GATEWAY_APNS_FILE=$apns|PUSH_GATEWAY_APNS_KEY_ID" \
  "PUSH_GATEWAY_DOMAIN=push.loomplane.test.invalid PUSH_GATEWAY_APNS_KEY_ID=ABCDE12345|PUSH_GATEWAY_APNS_FILE"; do
  vars=${case%%|*} want=${case#*|}
  # shellcheck disable=SC2086 # vars is a list of assignments on purpose
  if msg=$(gateway $vars 2>&1); then
    bad "push-gateway refuses '$vars'"
  elif [[ "$msg" == *"$want"* ]] && ! grep -q '^PUSH_GATEWAY_DOMAIN=push.loomplane' "$gw/.env"; then
    pass "push-gateway refuses '$vars'"
  else
    bad "push-gateway refuses '$vars' (message: $msg)"
  fi
done

# Without a Firebase key the gateway is prepared, with its sealing key, but off.
if gateway PUSH_GATEWAY_DOMAIN=push.loomplane.test.invalid >"$gwlog" 2>&1 \
  && grep -q 'prepared but off' "$gwlog" \
  && grep -q '^PUSH_GATEWAY_DOMAIN=push.loomplane.test.invalid$' "$gw/.env" \
  && ! grep -q '^COMPOSE_FILE=' "$gw/.env" \
  && [ -s "$gw/secrets/sealing-key" ] \
  && [ "$(stat -c '%u:%g:%a' "$gw/secrets/sealing-key")" = 10001:10001:400 ] \
  && [ "$(stat -c %a "$gw/secrets")" = 700 ]; then
  pass "push-gateway without a Firebase key prepares the gateway and leaves it off"
else
  bad "push-gateway without a Firebase key prepares the gateway and leaves it off"; cat "$gwlog" >&2
fi
seal=$(cat "$gw/secrets/sealing-key")

# With one it turns the overlay on, and keeps the sealing key.
if gateway PUSH_GATEWAY_DOMAIN=push.loomplane.test.invalid PUSH_GATEWAY_FCM_FILE="$fcm" >"$gwlog" 2>&1 \
  && grep -q '^COMPOSE_FILE=compose.yaml:compose.push-gateway.yaml$' "$gw/.env" \
  && [ "$(grep -c '^COMPOSE_FILE=' "$gw/.env")" = 1 ] \
  && cmp -s "$fcm" "$gw/secrets/fcm-service-account.json" \
  && [ "$(stat -c '%u:%g:%a' "$gw/secrets/fcm-service-account.json")" = 10001:10001:400 ] \
  && [ "$(cat "$gw/secrets/sealing-key")" = "$seal" ] \
  && [ -f "$gw/secrets/previous-sealing-key" ] && [ ! -s "$gw/secrets/previous-sealing-key" ] \
  && [ -f "$gw/secrets/apns-key.p8" ] && [ ! -s "$gw/secrets/apns-key.p8" ] \
  && [ "$(stat -c %a "$gw/.env")" = 600 ]; then
  pass "push-gateway with a Firebase key turns the gateway on and keeps its sealing key"
else
  bad "push-gateway with a Firebase key turns the gateway on and keeps its sealing key"; cat "$gwlog" >&2
fi

# An Apple key adds iOS; a rerun without keys changes nothing.
if gateway PUSH_GATEWAY_DOMAIN=push.loomplane.test.invalid PUSH_GATEWAY_APNS_FILE="$apns" \
  PUSH_GATEWAY_APNS_KEY_ID=ABCDE12345 PUSH_GATEWAY_APNS_TEAM_ID=TEAM123456 >"$gwlog" 2>&1 \
  && gateway PUSH_GATEWAY_DOMAIN=push.loomplane.test.invalid >>"$gwlog" 2>&1 \
  && grep -q '^PUSH_GATEWAY_APNS_KEY_ID=ABCDE12345$' "$gw/.env" \
  && grep -q '^PUSH_GATEWAY_APNS_TEAM_ID=TEAM123456$' "$gw/.env" \
  && cmp -s "$apns" "$gw/secrets/apns-key.p8" \
  && cmp -s "$fcm" "$gw/secrets/fcm-service-account.json" \
  && [ "$(cat "$gw/secrets/sealing-key")" = "$seal" ]; then
  pass "push-gateway adds an Apple key, and a rerun keeps every key"
else
  bad "push-gateway adds an Apple key, and a rerun keeps every key"; cat "$gwlog" >&2
fi

# all checks the gateway's hostname before it touches the system.
gwfresh=$work/gw-fresh
if msg=$(LOOMPLANE_DIR=$gwfresh LOOMPLANE_VERSION=v9.9.9 LOOMPLANE_DOMAIN=y.loomplane.test.invalid \
  ACME_EMAIL=ops@loomplane.test.invalid PUSH_GATEWAY_DOMAIN=push.example.com bash "$installer" all 2>&1); then
  bad "all refuses a placeholder gateway hostname"
elif [[ "$msg" == *"PUSH_GATEWAY_DOMAIN is still the example.com placeholder"* ]] && [ ! -e "$gwfresh" ]; then
  pass "all refuses a placeholder gateway hostname before creating the install dir"
else
  bad "all refuses a placeholder gateway hostname before creating the install dir (message: $msg)"
fi

# Hand the gateway install to the host, which checks Compose reads it.
rm -rf /out/gw
cp -R --no-preserve=ownership "$gw" /out/gw
chown -R "${LOOMPLANE_TEST_HOST_UID:-0}:${LOOMPLANE_TEST_HOST_GID:-0}" /out/gw

# 4f. runner checks everything before it changes the system, then hands
# node.sh the role, the token and its flags. A local https server stands in
# for Loomplane.
sha=$(printf '%064d' 0 | tr 0 a)
token=lpj_live_$(printf '%064d' 0 | tr 0 b)
kvm=$work/kvm
touch "$kvm"
runner_env=(LOOMPLANE_DIR="$work/runner-only" LOOMPLANE_KVM_DEVICE="$kvm" LOOMPLANE_RUNNER_CONFIG="$work/no-fc.json"
  LOOMPLANE_URL=https://localhost:8443 LOOMPLANE_JOIN_TOKEN="$token"
  LOOMPLANE_RUNNER_TOOLCHAIN_URL=https://releases.loomplane.test.invalid/tc.tar.gz LOOMPLANE_RUNNER_TOOLCHAIN_SHA256="$sha")
runner() { env "${runner_env[@]}" "$@" bash "$installer" runner; }
for case in "LOOMPLANE_URL=http://localhost:8443|LOOMPLANE_URL" \
  "LOOMPLANE_URL=https://loomplane.example.com|placeholder" \
  "LOOMPLANE_JOIN_TOKEN=lpj_...|LOOMPLANE_JOIN_TOKEN" \
  "LOOMPLANE_RUNNER_TOOLCHAIN_URL=|LOOMPLANE_RUNNER_TOOLCHAIN_URL" \
  "LOOMPLANE_RUNNER_TOOLCHAIN_URL=https://releases.example.com/tc.tar.gz|placeholder" \
  "LOOMPLANE_RUNNER_TOOLCHAIN_SHA256=abc|LOOMPLANE_RUNNER_TOOLCHAIN_SHA256" \
  "LOOMPLANE_RUNNER_RELAY_ALLOW=10.0.0.0/8,nope|RELAY_ALLOW" \
  "LOOMPLANE_RUNNER_NAME=Runner_1|LOOMPLANE_RUNNER_NAME" \
  "LOOMPLANE_DIR=$install_dir|Loomplane itself runs on this Droplet" \
  "LOOMPLANE_KVM_DEVICE=$work/no-kvm|can't run build microVMs"; do
  vars=${case%%|*} want=${case#*|}
  if msg=$(runner "$vars" 2>&1); then
    bad "runner refuses '$vars'"
  elif [[ "$msg" == *"$want"* ]] && [[ "$msg" != *"fetching the runner installer"* ]]; then
    pass "runner refuses '$vars'"
  else
    bad "runner refuses '$vars' (message: $msg)"
  fi
done

loom=$work/loom
mkdir -p "$loom/install"
cat >"$loom/install/node.sh" <<'NODE'
#!/bin/sh
{ echo "role=$LOOMPLANE_ROLE"; echo "token=$LOOMPLANE_JOIN_TOKEN"; for a in "$@"; do echo "arg=$a"; done; } >"$RUNNER_OUT"
NODE
openssl req -x509 -newkey rsa:2048 -nodes -subj /CN=localhost -addext subjectAltName=DNS:localhost -days 1 \
  -keyout "$work/tls.key" -out "$work/tls.crt" >/dev/null 2>&1
python3 - "$loom" "$work/tls.crt" "$work/tls.key" >/dev/null 2>&1 <<'PY' &
import functools, http.server, ssl, sys
root, crt, key = sys.argv[1:]
srv = http.server.ThreadingHTTPServer(("127.0.0.1", 8443), functools.partial(http.server.SimpleHTTPRequestHandler, directory=root))
ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
ctx.load_cert_chain(crt, key)
srv.socket = ctx.wrap_socket(srv.socket, server_side=True)
srv.serve_forever()
PY
tls=$!
for _ in $(seq 1 50); do curl -fs --cacert "$work/tls.crt" -o /dev/null https://localhost:8443/install/node.sh && break; sleep 0.1; done
out=$work/runner.out
if CURL_CA_BUNDLE=$work/tls.crt RUNNER_OUT=$out runner LOOMPLANE_RUNNER_NAME=runner-1 \
  LOOMPLANE_RUNNER_RELAY_ALLOW="10.116.0.0/20, 192.168.1.0/24" >"$work/runner.log" 2>&1 \
  && [ "$(cat "$out")" = "role=runner
token=$token
arg=--toolchain
arg=https://releases.loomplane.test.invalid/tc.tar.gz
arg=--toolchain-sha256
arg=$sha
arg=--name
arg=runner-1
arg=--relay-allow
arg=10.116.0.0/20
arg=--relay-allow
arg=192.168.1.0/24" ]; then
  pass "runner hands node.sh the role, the token and its flags"
else
  bad "runner hands node.sh the role, the token and its flags"; cat "$work/runner.log" "$out" >&2
fi
kill "$tls" 2>/dev/null || true

# 5. An unreachable release API fails clearly and creates nothing.
fresh=$work/never-created
if msg=$(LOOMPLANE_DIR=$fresh LOOMPLANE_VERSION='' LOOMPLANE_API_URL=http://127.0.0.1:9 \
  bash "$installer" prepare 2>&1); then
  bad "unreachable API fails"
elif [[ "$msg" == *"couldn't find a Loomplane release; set LOOMPLANE_VERSION"* ]] && [ ! -e "$fresh" ]; then
  pass "unreachable API fails and creates nothing"
else
  bad "unreachable API fails and creates nothing (message: $msg)"
fi

# 6. A tampered bundle is rejected.
tampered=$work/tampered
if msg=$(LOOMPLANE_DIR=$tampered LOOMPLANE_VERSION=v9.9.8 bash "$installer" prepare 2>&1); then
  bad "tampered bundle fails"
elif [[ "$msg" == *"checksum"* ]] && [ ! -e "$tampered/compose.yaml" ]; then
  pass "tampered bundle fails the checksum"
else
  bad "tampered bundle fails the checksum (message: $msg)"
fi

# 6b. A second install while one holds the lock fails fast.
export LOOMPLANE_LOCK_FILE=$work/install.lock
flock "$LOOMPLANE_LOCK_FILE" sleep 5 &
sleep 0.5
if msg=$(bash "$installer" secrets 2>&1); then
  bad "concurrent install is refused"
elif [[ "$msg" == *"another Loomplane install is running"* ]]; then
  pass "concurrent install is refused"
else
  bad "concurrent install is refused (message: $msg)"
fi
kill %2 2>/dev/null || true
unset LOOMPLANE_LOCK_FILE

# 7. A malformed version is refused before any download.
if LOOMPLANE_DIR=$work/badver LOOMPLANE_VERSION='v1/../x' bash "$installer" prepare >/dev/null 2>&1; then
  bad "malformed version fails"
else
  pass "malformed version fails"
fi

if [ "$fail" -ne 0 ]; then echo "test-install: FAIL" >&2; exit 1; fi
echo "test-install: PASS"
