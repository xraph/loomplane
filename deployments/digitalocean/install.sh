#!/usr/bin/env bash
# Shared Loomplane installer for DigitalOcean. Installs the production Docker
# Compose stack from a release bundle. Used by the Droplet cloud-init file,
# the Marketplace image build and the loomplane-setup wizard.
#
#   install.sh prepare    Docker, ufw, download and verify the release bundle
#   install.sh secrets    create .env, fill the secret key and DB password
#   install.sh configure  write LOOMPLANE_DOMAIN and ACME_EMAIL into .env
#   install.sh start      bring the stack up and print the setup link
#   install.sh all        prepare, secrets, configure, start
#
# Every step is safe to rerun and never overwrites an existing secret.
#
# Environment:
#   LOOMPLANE_VERSION                 release tag; empty means the latest stable release
#   LOOMPLANE_DOMAIN, ACME_EMAIL      written by `configure`
#   LOOMPLANE_DIR                     install root, default /srv/loomplane
#   LOOMPLANE_ANALYSIS_CONCURRENCY    written to .env by `configure` when set
set -euo pipefail

REPO="xraph/loomplane"
LOOMPLANE_DIR="${LOOMPLANE_DIR:-/srv/loomplane}"
LIB_DIR="/usr/local/lib/loomplane"

# Test hooks. Production runs never set these.
#   LOOMPLANE_RELEASE_BASE_URL  where release assets are downloaded from
#   LOOMPLANE_API_URL           GitHub API base used to find the latest release
#   LOOMPLANE_SKIP_DOCKER       skip Docker, ufw and image pulls (no Docker in the test container)
RELEASE_BASE_URL="${LOOMPLANE_RELEASE_BASE_URL:-https://github.com/${REPO}/releases/download}"
API_URL="${LOOMPLANE_API_URL:-https://api.github.com}"
SKIP_DOCKER="${LOOMPLANE_SKIP_DOCKER:-}"

# Fresh Ubuntu Droplets often have unattended-upgrades holding the dpkg lock.
APT_LOCK_WAIT=600
apt_get() { apt-get -o "DPkg::Lock::Timeout=$APT_LOCK_WAIT" "$@"; }
apt_update() {
  local try
  for try in 1 2 3 4; do
    apt_get update -qq && return 0
    log "apt-get update failed (attempt $try), retrying"
    sleep 5
  done
  die "apt-get update kept failing"
}

log() { echo "loomplane-install: $*" >&2; }
die() { log "$*"; exit 1; }

resolve_version() {
  local version="${LOOMPLANE_VERSION:-}"
  if [ -z "$version" ]; then
    local body
    body=$(curl -fsSL --max-time 20 --retry 2 "${API_URL}/repos/${REPO}/releases/latest" 2>/dev/null) || body=""
    version=$(printf '%s' "$body" | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n 1)
  fi
  [ -n "$version" ] || die "couldn't find a Loomplane release; set LOOMPLANE_VERSION"
  [[ "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+([-+][0-9A-Za-z.+-]+)?$ ]] \
    || die "LOOMPLANE_VERSION must look like v1.2.3, got: $version"
  printf '%s\n' "$version"
}

install_docker() {
  if docker compose version >/dev/null 2>&1; then
    log "Docker and the Compose plugin are already installed"
    return
  fi
  log "installing Docker Engine and the Compose plugin"
  export DEBIAN_FRONTEND=noninteractive
  apt_update
  apt_get install -y -qq ca-certificates curl
  install -m 0755 -d /etc/apt/keyrings
  curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc
  chmod a+r /etc/apt/keyrings/docker.asc
  # shellcheck disable=SC1091
  suite=$(. /etc/os-release && echo "${UBUNTU_CODENAME:-$VERSION_CODENAME}")
  cat >/etc/apt/sources.list.d/docker.sources <<DOCKER
Types: deb
URIs: https://download.docker.com/linux/ubuntu
Suites: ${suite}
Components: stable
Signed-By: /etc/apt/keyrings/docker.asc
DOCKER
  apt_update
  apt_get install -y -qq docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
  systemctl enable --now docker
}

configure_firewall() {
  log "allowing ports 22, 80 and 443 through ufw"
  export DEBIAN_FRONTEND=noninteractive
  apt_get install -y -qq ufw
  ufw allow 22/tcp >/dev/null
  ufw allow 80/tcp >/dev/null
  ufw allow 443/tcp >/dev/null
  ufw --force enable >/dev/null
}

download_bundle() {
  local version=$1 tarball="loomplane-$1-docker.tar.gz" tmp
  tmp=$(mktemp -d)
  # shellcheck disable=SC2064
  trap "rm -rf '$tmp'" RETURN
  log "downloading Loomplane $version"
  curl -fsSL --max-time 300 --retry 3 -o "$tmp/$tarball" "${RELEASE_BASE_URL}/${version}/${tarball}" \
    || die "couldn't download $tarball for $version"
  curl -fsSL --max-time 60 --retry 3 -o "$tmp/SHA256SUMS" "${RELEASE_BASE_URL}/${version}/SHA256SUMS" \
    || die "couldn't download SHA256SUMS for $version"
  # SHA256SUMS lists the tarball by basename; check only that line.
  grep -F "  $tarball" "$tmp/SHA256SUMS" >"$tmp/check" \
    || die "SHA256SUMS has no entry for $tarball"
  (cd "$tmp" && sha256sum -c check >/dev/null 2>&1) \
    || die "checksum mismatch for $tarball; refusing to install it"
  mkdir -p "$LOOMPLANE_DIR"
  # The bundle is built by a CI user (uid 1001). Never keep its owner or
  # modes: that uid is the second user anyone creates on a Droplet.
  tar --no-same-owner --no-same-permissions -xzf "$tmp/$tarball" -C "$LOOMPLANE_DIR"
  chown -R root:root "$LOOMPLANE_DIR"
  chmod 0755 "$LOOMPLANE_DIR"
  find "$LOOMPLANE_DIR" -mindepth 1 -type d -exec chmod 0755 {} +
  find "$LOOMPLANE_DIR" -mindepth 1 -type f ! -name .env -exec chmod 0644 {} +
  if [ -f "$LOOMPLANE_DIR/.env" ]; then chmod 0600 "$LOOMPLANE_DIR/.env"; fi
}

pull_images() {
  log "pulling images"
  # Compose insists on these variables even for a pull. Placeholders live in
  # the environment for this one command and are never written anywhere.
  (
    cd "$LOOMPLANE_DIR"
    LOOMPLANE_DOMAIN=placeholder.invalid ACME_EMAIL=placeholder@placeholder.invalid \
      LOOMPLANE_SECRET_KEY=placeholder POSTGRES_PASSWORD=placeholder \
      docker compose --env-file .env.example pull --quiet
  )
}

# Keep a copy of this script where loomplane-setup can find it.
keep_installer() {
  local self="${BASH_SOURCE[0]}"
  [ -f "$self" ] || return 0
  mkdir -p "$LIB_DIR"
  if [ "$(readlink -f "$self")" != "$LIB_DIR/install.sh" ]; then
    install -m 0755 "$self" "$LIB_DIR/install.sh"
  fi
}

step_prepare() {
  local version
  version=$(resolve_version)
  if [ -z "$SKIP_DOCKER" ]; then
    install_docker
    configure_firewall
  fi
  download_bundle "$version"
  if [ -z "$SKIP_DOCKER" ]; then
    pull_images
  fi
  keep_installer
  log "Loomplane $version is in $LOOMPLANE_DIR"
}

# get_env KEY prints the value of KEY in .env, or nothing.
get_env() {
  sed -n "s/^$1=//p" "$LOOMPLANE_DIR/.env" | head -n 1
}

# set_env KEY VALUE replaces KEY's line in .env, or appends one. The value
# goes through the environment so no character in it needs escaping.
set_env() {
  local file="$LOOMPLANE_DIR/.env" tmp
  tmp=$(mktemp "$LOOMPLANE_DIR/.env.XXXXXX")
  KEY="$1" VALUE="$2" awk '
    BEGIN { k = ENVIRON["KEY"]; v = ENVIRON["VALUE"]; done = 0 }
    index($0, k "=") == 1 && !done { print k "=" v; done = 1; next }
    { print }
    END { if (!done) print k "=" v }
  ' "$file" >"$tmp"
  chmod 600 "$tmp"
  mv "$tmp" "$file"
}

step_secrets() {
  [ -f "$LOOMPLANE_DIR/.env.example" ] || die "no bundle in $LOOMPLANE_DIR; run 'install.sh prepare' first"
  umask 077
  if [ ! -f "$LOOMPLANE_DIR/.env" ]; then
    cp "$LOOMPLANE_DIR/.env.example" "$LOOMPLANE_DIR/.env"
  fi
  chmod 600 "$LOOMPLANE_DIR/.env"
  if [ -z "$(get_env LOOMPLANE_SECRET_KEY)" ]; then
    set_env LOOMPLANE_SECRET_KEY "$(openssl rand -base64 32)"
    log "generated LOOMPLANE_SECRET_KEY"
  fi
  if [ -z "$(get_env POSTGRES_PASSWORD)" ]; then
    set_env POSTGRES_PASSWORD "$(openssl rand -hex 32)"
    log "generated POSTGRES_PASSWORD"
  fi
}

valid_hostname() {
  local host=$1
  [ "${#host}" -le 253 ] || return 1
  [[ "$host" =~ ^([A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?\.)+[A-Za-z]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?$ ]]
}

step_configure() {
  local domain="${LOOMPLANE_DOMAIN:-}" email="${ACME_EMAIL:-}"
  [ -f "$LOOMPLANE_DIR/.env" ] || die "no .env in $LOOMPLANE_DIR; run 'install.sh secrets' first"
  [ -n "$domain" ] || die "set LOOMPLANE_DOMAIN to the hostname Loomplane will answer on"
  valid_hostname "$domain" \
    || die "LOOMPLANE_DOMAIN must be a bare hostname such as loomplane.example.com, not a URL or path (got: $domain)"
  [[ "$email" =~ ^[^[:space:]@]+@[^[:space:]@]+\.[^[:space:]@]+$ ]] \
    || die "set ACME_EMAIL to a valid email address for certificate notices"
  [[ "${LOOMPLANE_ANALYSIS_CONCURRENCY:-1}" =~ ^[0-9]+$ ]] \
    || die "LOOMPLANE_ANALYSIS_CONCURRENCY must be a whole number"
  set_env LOOMPLANE_DOMAIN "$domain"
  set_env ACME_EMAIL "$email"
  if [ -n "${LOOMPLANE_ANALYSIS_CONCURRENCY:-}" ]; then
    set_env LOOMPLANE_ANALYSIS_CONCURRENCY "$LOOMPLANE_ANALYSIS_CONCURRENCY"
  fi
  log "configured $domain"
}

step_start() {
  cd "$LOOMPLANE_DIR"
  [ -f .env ] || die "no .env in $LOOMPLANE_DIR; run 'install.sh secrets' first"
  log "starting Loomplane"
  docker compose up -d --wait --wait-timeout 240
  touch "$LOOMPLANE_DIR/.configured"
  docker compose exec -T loomplane loomplane setup-token \
    || log "couldn't read the setup link; run: cd $LOOMPLANE_DIR && docker compose exec -T loomplane loomplane setup-token"
  log ".env holds your secret key. Copy $LOOMPLANE_DIR/.env somewhere safe: without it stored credentials can't be recovered."
}

# Hold one lock for the whole run so cloud-init and loomplane-setup can't
# install at the same time. The test hook LOOMPLANE_LOCK_FILE moves the lock.
take_lock() {
  local lock="${LOOMPLANE_LOCK_FILE:-/run/loomplane-install.lock}"
  exec 9>"$lock" || die "couldn't open $lock"
  flock -n 9 || die "another Loomplane install is running (lock $lock); wait for it to finish"
}

main() {
  case "${1:-}" in
    prepare|secrets|configure|start|all) take_lock ;;
  esac
  case "${1:-}" in
    prepare) step_prepare ;;
    secrets) step_secrets ;;
    configure) step_configure ;;
    start) step_start ;;
    all) step_prepare; step_secrets; step_configure; step_start ;;
    *) echo "usage: install.sh prepare|secrets|configure|start|all" >&2; exit 2 ;;
  esac
}

main "$@"
