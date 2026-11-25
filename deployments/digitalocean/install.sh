#!/usr/bin/env bash
# Shared Loomplane installer for DigitalOcean. Installs the production Docker
# Compose stack from a release bundle. Used by the Droplet cloud-init file,
# the Marketplace image build and the loomplane-setup wizard.
#
#   install.sh prepare    Docker, ufw, download and verify the release bundle
#   install.sh secrets    create .env, fill the secret key and DB password
#   install.sh configure  write LOOMPLANE_DOMAIN and ACME_EMAIL into .env
#   install.sh start         bring the stack up and print the setup link
#   install.sh push-gateway  run the push gateway beside Loomplane
#   install.sh all           prepare, secrets, configure, push-gateway when
#                            PUSH_GATEWAY_DOMAIN is set, start
#   install.sh runner        make this Droplet a runner: it builds in
#                            Firecracker microVMs. Run it on a Droplet of its own.
#
# Every step is safe to rerun and never overwrites an existing secret.
#
# Environment:
#   LOOMPLANE_VERSION                 release tag; empty means the latest stable release
#   LOOMPLANE_DOMAIN, ACME_EMAIL      written by `configure`
#   LOOMPLANE_DIR                     install root, default /srv/loomplane
#   LOOMPLANE_ANALYSIS_CONCURRENCY    written to .env by `configure` when set
#
# push-gateway:
#   PUSH_GATEWAY_DOMAIN               the gateway's own hostname
#   PUSH_GATEWAY_FCM_FILE             a Firebase service-account JSON key to install.
#                                     Without one the gateway is prepared, not started.
#   PUSH_GATEWAY_APNS_FILE            optional Apple .p8 key, with
#   PUSH_GATEWAY_APNS_KEY_ID, PUSH_GATEWAY_APNS_TEAM_ID and optionally
#   PUSH_GATEWAY_APNS_BUNDLE_ID
#
# runner:
#   LOOMPLANE_URL                     where Loomplane is, such as https://loomplane.example.com
#   LOOMPLANE_JOIN_TOKEN              a runner join token (Runners, Add runner); it lasts an hour
#   LOOMPLANE_RUNNER_TOOLCHAIN_URL, LOOMPLANE_RUNNER_TOOLCHAIN_SHA256
#                                     the Firecracker toolchain bundle and its sha256
#   LOOMPLANE_RUNNER_NAME             optional; the hostname otherwise
#   LOOMPLANE_RUNNER_RELAY_ALLOW      optional comma-separated CIDRs deploys may be relayed to
set -euo pipefail

REPO="xraph/loomplane"
LOOMPLANE_DIR="${LOOMPLANE_DIR:-/srv/loomplane}"
LIB_DIR="/usr/local/lib/loomplane"

# Test hooks. Production runs never set these.
#   LOOMPLANE_RELEASE_BASE_URL  where release assets are downloaded from
#   LOOMPLANE_API_URL           GitHub API base used to find the latest release
#   LOOMPLANE_SKIP_DOCKER       skip Docker, ufw, image pulls and the runner's host
#                               setup (no Docker or KVM in the test container)
#   LOOMPLANE_KVM_DEVICE        the device a runner checks for
#   LOOMPLANE_RUNNER_CONFIG     the runner's Firecracker host config
RELEASE_BASE_URL="${LOOMPLANE_RELEASE_BASE_URL:-https://github.com/${REPO}/releases/download}"
API_URL="${LOOMPLANE_API_URL:-https://api.github.com}"
SKIP_DOCKER="${LOOMPLANE_SKIP_DOCKER:-}"
KVM_DEVICE="${LOOMPLANE_KVM_DEVICE:-/dev/kvm}"
RUNNER_CONFIG="${LOOMPLANE_RUNNER_CONFIG:-/etc/loomplane-runner/firecracker.json}"

# The gateway image runs as this user, and reads its keys as it.
GATEWAY_UID=10001
# The subnet a runner's build microVMs use, unless its host config says.
RUNNER_SUBNET=10.231.0.0/20

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
  mkdir -p "$LOOMPLANE_DIR" "$tmp/bundle"
  # The bundle is built by a CI user (uid 1001). Never keep its owner or
  # modes: that uid is the second user anyone creates on a Droplet.
  # Normalize only the extracted bundle. Existing gateway keys may use a
  # custom directory and must keep their ownership and permissions.
  tar --no-same-owner --no-same-permissions -xzf "$tmp/$tarball" -C "$tmp/bundle"
  chown -R root:root "$tmp/bundle"
  find "$tmp/bundle" -type d -exec chmod 0755 {} +
  find "$tmp/bundle" -type f ! -name '.env*' -exec chmod 0644 {} +
  cp -a "$tmp/bundle/." "$LOOMPLANE_DIR/"
  chmod 0755 "$LOOMPLANE_DIR"
  # .env.example is the one .env* file meant to be readable.
  chmod 0644 "$LOOMPLANE_DIR/.env.example" 2>/dev/null || true
  find "$LOOMPLANE_DIR" -maxdepth 1 -type f -name '.env.*' ! -name .env.example -exec chmod 0600 {} +
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

# The bundle and its image are a pair. On an upgrade, point an existing .env
# at the image the new bundle ships with. Secrets and the domain stay put.
sync_image() {
  [ -f "$LOOMPLANE_DIR/.env" ] || return 0
  local image
  image=$(sed -n 's/^LOOMPLANE_IMAGE=//p' "$LOOMPLANE_DIR/.env.example" | head -n 1)
  [ -n "$image" ] || return 0
  set_env LOOMPLANE_IMAGE "$image"
  log "set LOOMPLANE_IMAGE to $image"
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
  sync_image
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
  # shellcheck disable=SC2064
  trap "rm -f '$tmp'" RETURN
  KEY="$1" VALUE="$2" awk '
    BEGIN { k = ENVIRON["KEY"]; v = ENVIRON["VALUE"]; done = 0 }
    index($0, k "=") == 1 && !done { print k "=" v; done = 1; next }
    { print }
    END { if (!done) print k "=" v }
  ' "$file" >"$tmp" || die "couldn't update $file"
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

# check_config validates the domain, email and concurrency without touching
# the system, so `all` can fail before it installs anything.
check_config() {
  local domain="${LOOMPLANE_DOMAIN:-}" email="${ACME_EMAIL:-}"
  [ -n "$domain" ] || die "set LOOMPLANE_DOMAIN to the hostname Loomplane will answer on"
  valid_hostname "$domain" \
    || die "LOOMPLANE_DOMAIN must be a bare hostname such as loomplane.example.com, not a URL or path (got: $domain)"
  [[ "$email" =~ ^[^[:space:]@]+@[^[:space:]@]+\.[^[:space:]@]+$ ]] \
    || die "set ACME_EMAIL to a valid email address for certificate notices"
  case "$domain" in
    example.com|*.example.com)
      die "LOOMPLANE_DOMAIN is still the example.com placeholder; set the hostname you'll use (got: $domain)" ;;
  esac
  case "$email" in
    *@example.com)
      die "ACME_EMAIL is still the example.com placeholder; set a real address for certificate notices (got: $email)" ;;
  esac
  [[ "${LOOMPLANE_ANALYSIS_CONCURRENCY:-1}" =~ ^[1-9][0-9]*$ ]] \
    || die "LOOMPLANE_ANALYSIS_CONCURRENCY must be a whole number of at least 1"
}

step_configure() {
  local domain="${LOOMPLANE_DOMAIN:-}" email="${ACME_EMAIL:-}"
  [ -f "$LOOMPLANE_DIR/.env" ] || die "no .env in $LOOMPLANE_DIR; run 'install.sh secrets' first"
  check_config
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

# gateway_dir is where the gateway's keys live: PUSH_GATEWAY_SECRETS_DIR,
# relative to the install, or secrets/.
gateway_dir() {
  local d
  d=$(get_env PUSH_GATEWAY_SECRETS_DIR)
  d=${d:-./secrets}
  case "$d" in
    /*) printf '%s\n' "$d" ;;
    *) printf '%s\n' "$LOOMPLANE_DIR/${d#./}" ;;
  esac
}

# put_key SRC NAME installs SRC as the gateway key NAME, readable only by
# the gateway's user.
put_key() {
  local dest
  dest="$(gateway_dir)/$2"
  if [ "$(readlink -f "$1")" != "$(readlink -f "$dest" 2>/dev/null || true)" ]; then
    install -m 0400 -o "$GATEWAY_UID" -g "$GATEWAY_UID" "$1" "$dest"
  fi
  chown "$GATEWAY_UID:$GATEWAY_UID" "$dest"
  chmod 0400 "$dest"
}

# apple_id checks a 10-character Apple key or team ID.
apple_id() { [[ "$1" =~ ^[A-Z0-9]{10}$ ]]; }

# check_gateway_config validates the gateway's settings and key files
# without touching anything. Loomplane's own domain comes from the
# environment, or .env once there is one.
check_gateway_config() {
  local domain="${PUSH_GATEWAY_DOMAIN:-}" own="${LOOMPLANE_DOMAIN:-}"
  if [ -z "$own" ] && [ -f "$LOOMPLANE_DIR/.env" ]; then own=$(get_env LOOMPLANE_DOMAIN); fi
  [ -n "$domain" ] || die "set PUSH_GATEWAY_DOMAIN to the gateway's own hostname, such as push.loomplane.example.com"
  valid_hostname "$domain" \
    || die "PUSH_GATEWAY_DOMAIN must be a bare hostname such as push.loomplane.example.com, not a URL or path (got: $domain)"
  case "$domain" in
    example.com|*.example.com)
      die "PUSH_GATEWAY_DOMAIN is still the example.com placeholder; set the gateway's hostname (got: $domain)" ;;
  esac
  [ "$domain" != "$own" ] || die "PUSH_GATEWAY_DOMAIN must be a hostname of its own, not LOOMPLANE_DOMAIN"
  local fcm="${PUSH_GATEWAY_FCM_FILE:-}" apns="${PUSH_GATEWAY_APNS_FILE:-}"
  if [ -n "$fcm" ]; then
    [ -r "$fcm" ] || die "can't read PUSH_GATEWAY_FCM_FILE $fcm"
    if ! grep -q '"private_key"' "$fcm" || ! grep -q '"client_email"' "$fcm"; then
      die "PUSH_GATEWAY_FCM_FILE $fcm isn't a Firebase service-account JSON key"
    fi
  fi
  if [ -n "$apns" ]; then
    [ -r "$apns" ] || die "can't read PUSH_GATEWAY_APNS_FILE $apns"
    grep -q 'BEGIN PRIVATE KEY' "$apns" || die "PUSH_GATEWAY_APNS_FILE $apns isn't an Apple .p8 key"
    if ! apple_id "${PUSH_GATEWAY_APNS_KEY_ID:-}" || ! apple_id "${PUSH_GATEWAY_APNS_TEAM_ID:-}"; then
      die "an Apple key needs PUSH_GATEWAY_APNS_KEY_ID and PUSH_GATEWAY_APNS_TEAM_ID, 10 letters and digits each"
    fi
  elif [ -n "${PUSH_GATEWAY_APNS_KEY_ID:-}${PUSH_GATEWAY_APNS_TEAM_ID:-}" ]; then
    die "set PUSH_GATEWAY_APNS_FILE to the Apple .p8 key along with its key and team IDs"
  fi
}

step_push_gateway() {
  [ -f "$LOOMPLANE_DIR/.env" ] || die "no .env in $LOOMPLANE_DIR; run 'install.sh secrets' first"
  check_gateway_config
  local domain="$PUSH_GATEWAY_DOMAIN" dir
  dir=$(gateway_dir)
  umask 077
  mkdir -p "$dir"
  chmod 0700 "$dir"
  if [ ! -s "$dir/sealing-key" ]; then
    local tmp
    tmp=$(mktemp "$dir/.sealing-key.XXXXXX")
    openssl rand -base64 32 >"$tmp"
    chown "$GATEWAY_UID:$GATEWAY_UID" "$tmp"
    chmod 0400 "$tmp"
    mv "$tmp" "$dir/sealing-key"
    log "generated the gateway's sealing key in $dir/sealing-key. Back it up with .env: replacing it breaks every registered phone"
  fi
  # Compose mounts every key file, so the unused ones exist, empty.
  local f
  for f in previous-sealing-key apns-key.p8 fcm-service-account.json; do
    if [ ! -e "$dir/$f" ]; then
      : >"$dir/$f"
      chown "$GATEWAY_UID:$GATEWAY_UID" "$dir/$f"
      chmod 0400 "$dir/$f"
    fi
  done
  if [ -n "${PUSH_GATEWAY_FCM_FILE:-}" ]; then
    put_key "$PUSH_GATEWAY_FCM_FILE" fcm-service-account.json
    log "installed the Firebase key"
  fi
  if [ -n "${PUSH_GATEWAY_APNS_FILE:-}" ]; then
    put_key "$PUSH_GATEWAY_APNS_FILE" apns-key.p8
    set_env PUSH_GATEWAY_APNS_KEY_ID "$PUSH_GATEWAY_APNS_KEY_ID"
    set_env PUSH_GATEWAY_APNS_TEAM_ID "$PUSH_GATEWAY_APNS_TEAM_ID"
    if [ -n "${PUSH_GATEWAY_APNS_BUNDLE_ID:-}" ]; then
      set_env PUSH_GATEWAY_APNS_BUNDLE_ID "$PUSH_GATEWAY_APNS_BUNDLE_ID"
    fi
    log "installed the Apple key"
  fi
  set_env PUSH_GATEWAY_DOMAIN "$domain"
  if [ -n "$(get_env PUSH_GATEWAY_APNS_KEY_ID)" ] && [ ! -s "$dir/apns-key.p8" ]; then
    die "PUSH_GATEWAY_APNS_KEY_ID is set in .env but $dir/apns-key.p8 is empty; set PUSH_GATEWAY_APNS_FILE"
  fi
  if [ ! -s "$dir/fcm-service-account.json" ]; then
    log "the gateway is prepared but off: it needs your Firebase service-account key. Copy the key to this Droplet, then run:"
    log "  PUSH_GATEWAY_DOMAIN=$domain PUSH_GATEWAY_FCM_FILE=/path/to/key.json $LIB_DIR/install.sh push-gateway"
    return 0
  fi
  set_env COMPOSE_FILE compose.yaml:compose.push-gateway.yaml
  log "the push gateway answers on https://$domain. Point an A record for it at this Droplet"
  if [ -z "$SKIP_DOCKER" ] && [ -f "$LOOMPLANE_DIR/.configured" ]; then
    log "starting the push gateway"
    (cd "$LOOMPLANE_DIR" && docker compose up -d --wait --wait-timeout 240)
  fi
}

# runner_url is where the runner reaches Loomplane, without a trailing slash.
runner_url() { printf '%s\n' "${LOOMPLANE_URL%/}"; }

# check_runner_config validates what a runner needs without touching the
# system, so a bad value fails before anything is installed.
check_runner_config() {
  local url token sha
  url=$(runner_url)
  token="${LOOMPLANE_JOIN_TOKEN:-}"
  [[ "$url" =~ ^https://[A-Za-z0-9.-]+(:[0-9]+)?$ ]] \
    || die "set LOOMPLANE_URL to Loomplane's address, such as https://loomplane.example.com (got: ${LOOMPLANE_URL:-})"
  case "$url" in
    https://example.com|https://*.example.com|https://example.com:*|https://*.example.com:*)
      die "LOOMPLANE_URL is still the example.com placeholder; set your Loomplane's address (got: $url)" ;;
  esac
  [[ "$token" =~ ^lpj_[a-z]+_[0-9a-f]{64}$ ]] \
    || die "set LOOMPLANE_JOIN_TOKEN to a runner join token from Runners, Add runner; it starts lpj_ and lasts an hour"
  if [ -n "${LOOMPLANE_RUNNER_TOOLCHAIN_URL:-}" ] || [ ! -f "$RUNNER_CONFIG" ]; then
    [[ "${LOOMPLANE_RUNNER_TOOLCHAIN_URL:-}" =~ ^https://[^[:space:]]+$ ]] \
      || die "set LOOMPLANE_RUNNER_TOOLCHAIN_URL to the https URL of your Firecracker toolchain bundle (deployments/firecracker/build-runner-toolchain.sh builds one)"
    case "$LOOMPLANE_RUNNER_TOOLCHAIN_URL" in
      https://example.com/*|https://*.example.com/*)
        die "LOOMPLANE_RUNNER_TOOLCHAIN_URL is still the example.com placeholder; set where your toolchain bundle is (got: $LOOMPLANE_RUNNER_TOOLCHAIN_URL)" ;;
    esac
    sha=$(printf '%s' "${LOOMPLANE_RUNNER_TOOLCHAIN_SHA256:-}" | tr 'A-F' 'a-f')
    [[ "$sha" =~ ^[0-9a-f]{64}$ ]] \
      || die "set LOOMPLANE_RUNNER_TOOLCHAIN_SHA256 to the bundle's sha256, 64 hex characters"
  fi
  local cidr
  local -a cidrs=()
  IFS=',' read -r -a cidrs <<<"${LOOMPLANE_RUNNER_RELAY_ALLOW:-}"
  for cidr in "${cidrs[@]}"; do
    cidr=$(printf '%s' "$cidr" | tr -d '[:space:]')
    [ -z "$cidr" ] || [[ "$cidr" =~ ^[0-9A-Fa-f.:]+/[0-9]{1,3}$ ]] \
      || die "LOOMPLANE_RUNNER_RELAY_ALLOW takes CIDRs such as 10.0.0.0/16, comma-separated (got: $cidr)"
  done
  [[ "${LOOMPLANE_RUNNER_NAME:-}" =~ ^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)?$ ]] \
    || die "LOOMPLANE_RUNNER_NAME must be a lowercase DNS label, such as runner-1"
  # Loomplane itself is the one thing a runner never shares a Droplet with.
  [ ! -f "$LOOMPLANE_DIR/.env" ] \
    || die "Loomplane itself runs on this Droplet. Give runners Droplets of their own: a runner sizes its build microVMs to the whole machine, and builds run code from your repositories. Create one from cloud-init.runner.yaml"
  [ -e "$KVM_DEVICE" ] \
    || die "this Droplet has no $KVM_DEVICE, so it can't run build microVMs. Use a regular Ubuntu 24.04 Droplet: they have nested virtualization on"
}

# prepare_runner_host installs what the toolchain doesn't bring, turns SMT
# off so build VMs never share a core, and lets only SSH in. ufw drops
# forwarded traffic, so the build subnet is let out explicitly.
prepare_runner_host() {
  export DEBIAN_FRONTEND=noninteractive
  log "installing the runner's host tools"
  apt_update
  apt_get install -y -qq ca-certificates curl nftables iproute2 e2fsprogs ufw
  cat >/etc/systemd/system/loomplane-nosmt.service <<'UNIT'
[Unit]
Description=Turn SMT off so Loomplane build microVMs never share a CPU core
Before=loomplane-runner.service
DefaultDependencies=no
After=sysinit.target

[Service]
Type=oneshot
# systemd expands $ itself, so the shell's are doubled.
ExecStart=/bin/sh -c 'c=/sys/devices/system/cpu/smt/control; [ ! -w "$$c" ] || [ "$$(cat "$$c")" = notsupported ] || echo off >"$$c"'
RemainAfterExit=yes

[Install]
WantedBy=multi-user.target
UNIT
  systemctl daemon-reload
  systemctl enable --now loomplane-nosmt.service
  printf 'net.ipv4.ip_forward = 1\n' >/etc/sysctl.d/90-loomplane-runner.conf
  sysctl -q -p /etc/sysctl.d/90-loomplane-runner.conf
  local subnet
  subnet=$(sed -n 's/.*"Subnet"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$RUNNER_CONFIG" 2>/dev/null | head -n 1)
  subnet=${subnet:-$RUNNER_SUBNET}
  log "allowing SSH in, and build microVMs in $subnet out, through ufw"
  ufw allow 22/tcp >/dev/null
  ufw route allow from "$subnet" >/dev/null
  ufw --force enable >/dev/null
}

step_runner() {
  check_runner_config
  if [ -z "$SKIP_DOCKER" ]; then
    prepare_runner_host
  fi
  local url tmp cidr
  local -a args=() cidrs=()
  url=$(runner_url)
  if [ -n "${LOOMPLANE_RUNNER_TOOLCHAIN_URL:-}" ]; then
    args+=(--toolchain "$LOOMPLANE_RUNNER_TOOLCHAIN_URL" --toolchain-sha256 "$(printf '%s' "$LOOMPLANE_RUNNER_TOOLCHAIN_SHA256" | tr 'A-F' 'a-f')")
  fi
  if [ -n "${LOOMPLANE_RUNNER_NAME:-}" ]; then args+=(--name "$LOOMPLANE_RUNNER_NAME"); fi
  IFS=',' read -r -a cidrs <<<"${LOOMPLANE_RUNNER_RELAY_ALLOW:-}"
  for cidr in "${cidrs[@]}"; do
    cidr=$(printf '%s' "$cidr" | tr -d '[:space:]')
    if [ -n "$cidr" ]; then args+=(--relay-allow "$cidr"); fi
  done
  tmp=$(mktemp -d)
  # shellcheck disable=SC2064
  trap "rm -rf '$tmp'" RETURN
  log "fetching the runner installer from $url"
  curl --proto '=https' -fsSL --max-time 60 --retry 3 -o "$tmp/node.sh" "$url/install/node.sh" \
    || die "couldn't fetch $url/install/node.sh; check LOOMPLANE_URL and that Loomplane answers there"
  # node.sh installs the signed loomctl and runs `runner start`, which
  # installs the toolchain, joins and starts the loomplane-runner service.
  LOOMPLANE_ROLE=runner LOOMPLANE_JOIN_TOKEN="$LOOMPLANE_JOIN_TOKEN" sh "$tmp/node.sh" "${args[@]}"
  log "this Droplet is a runner. Check it with: loomctl runner status"
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
    prepare|secrets|configure|start|push-gateway|all|runner) take_lock ;;
  esac
  case "${1:-}" in
    prepare) step_prepare ;;
    secrets) step_secrets ;;
    configure) step_configure ;;
    start) step_start ;;
    push-gateway) step_push_gateway ;;
    runner) step_runner ;;
    all)
      check_config
      if [ -n "${PUSH_GATEWAY_DOMAIN:-}" ]; then check_gateway_config; fi
      step_prepare
      step_secrets
      step_configure
      if [ -n "${PUSH_GATEWAY_DOMAIN:-}" ]; then step_push_gateway; fi
      step_start
      ;;
    *) echo "usage: install.sh prepare|secrets|configure|start|push-gateway|all|runner" >&2; exit 2 ;;
  esac
}

main "$@"
