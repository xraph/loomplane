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
    -v "$repo:/repo:ro" -v "$out:/out" ubuntu:24.04 bash /repo/deployments/digitalocean/test-install.sh
  echo "test-install: docker compose config on the installed bundle"
  docker compose --project-directory "$out/install" config --quiet
  echo "test-install: PASS (host compose config ok)"
  exit 0
fi

# ---- inside the container ----
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq >/dev/null
apt-get install -y -qq curl python3 openssl ca-certificates >/dev/null

installer=/repo/deployments/digitalocean/install.sh
work=$(mktemp -d)
srv=$work/srv
fail=0

pass() { echo "ok   $1"; }
bad() { echo "FAIL $1" >&2; fail=1; }

# Build the fixture the way publish-docker.yml builds the release bundle.
build_bundle() {
  local tag=$1 dir=$srv/$1 bundle=$work/bundle-$1
  mkdir -p "$dir" "$bundle"
  cp /repo/deployments/production/compose.yaml /repo/deployments/production/Caddyfile \
    /repo/deployments/production/.env.example "$bundle/"
  cp /repo/docs/docker-production.md "$bundle/README.md"
  echo "ghcr.io/xraph/loomplane@sha256:0000" >"$bundle/image.txt"
  sed -i "s|^LOOMPLANE_IMAGE=.*|LOOMPLANE_IMAGE=ghcr.io/xraph/loomplane@sha256:0000|" "$bundle/.env.example"
  tar -czf "$dir/loomplane-$tag-docker.tar.gz" -C "$bundle" .
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
for bad_domain in "https://x.example.com/" "x.example.com/path"; do
  if msg=$(LOOMPLANE_DOMAIN=$bad_domain ACME_EMAIL=ops@example.com bash "$installer" configure 2>&1); then
    bad "configure rejects $bad_domain"
  elif [[ "$msg" == *"bare hostname"* ]]; then
    pass "configure rejects $bad_domain"
  else
    bad "configure rejects $bad_domain (message: $msg)"
  fi
done

# 4. configure writes the domain, email and concurrency.
if LOOMPLANE_DOMAIN=x.example.com ACME_EMAIL=ops@example.com LOOMPLANE_ANALYSIS_CONCURRENCY=3 \
  bash "$installer" configure >/dev/null 2>&1 \
  && grep -q '^LOOMPLANE_DOMAIN=x.example.com$' "$install_dir/.env" \
  && grep -q '^ACME_EMAIL=ops@example.com$' "$install_dir/.env" \
  && grep -q '^LOOMPLANE_ANALYSIS_CONCURRENCY=3$' "$install_dir/.env" \
  && [ "$(grep -c '^LOOMPLANE_SECRET_KEY=' "$install_dir/.env")" = 1 ] \
  && [ "$(grep '^LOOMPLANE_SECRET_KEY=' "$install_dir/.env")" = "$key1" ]; then
  pass "configure writes .env"
else
  bad "configure writes .env"
fi

# Keep the finished install for the host's `docker compose config` check.
rm -rf /out/install
cp -a "$install_dir" /out/install

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

# 7. A malformed version is refused before any download.
if LOOMPLANE_DIR=$work/badver LOOMPLANE_VERSION='v1/../x' bash "$installer" prepare >/dev/null 2>&1; then
  bad "malformed version fails"
else
  pass "malformed version fails"
fi

if [ "$fail" -ne 0 ]; then echo "test-install: FAIL" >&2; exit 1; fi
echo "test-install: PASS"
