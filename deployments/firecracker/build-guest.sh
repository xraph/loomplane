#!/usr/bin/env bash
set -euo pipefail
# Run on a disposable Linux image-builder. Only trusted rootfs inputs are used.
: "${GUEST_BASE:?Set GUEST_BASE to a digest-pinned Debian OCI image}"
: "${NERDCTL_SHA256:?Set the reviewed nerdctl-full archive SHA256}"
: "${OUTPUT:?Set OUTPUT to a new absolute ext4 image path}"
ARCH="${ARCH:-amd64}"
case "$ARCH" in amd64|arm64) ;; *) exit 2 ;; esac
case "$GUEST_BASE" in *@sha256:*) ;; *) echo 'GUEST_BASE must use a digest' >&2; exit 2 ;; esac
[[ "$OUTPUT" = /* && ! -e "$OUTPUT" ]]
[[ "$NERDCTL_SHA256" =~ ^[a-f0-9]{64}$ ]]
root="$(mktemp -d)"
container=''
cleanup() { if [[ -n "$container" ]]; then docker rm "$container" >/dev/null; fi; rm -rf "$root"; }
trap cleanup EXIT
CGO_ENABLED=0 GOOS=linux GOARCH="$ARCH" go build -trimpath -o "$root/loomplane-guest-agent" ./cmd/loomplane-guest-agent
curl --fail --location --proto '=https' --tlsv1.2 "https://github.com/containerd/nerdctl/releases/download/v2.4.1/nerdctl-full-2.4.1-linux-$ARCH.tar.gz" -o "$root/nerdctl.tar.gz"
printf '%s  %s\n' "$NERDCTL_SHA256" "$root/nerdctl.tar.gz" | sha256sum -c -
mkdir "$root/fs"
container="$(docker create --platform "linux/$ARCH" "$GUEST_BASE")"
docker export "$container" | tar -x -C "$root/fs"
tar -xzf "$root/nerdctl.tar.gz" -C "$root/fs/usr/local"
install -m 755 "$root/loomplane-guest-agent" "$root/fs/usr/local/bin/loomplane-guest-agent"
install -m 755 deployments/firecracker/guest-init "$root/fs/sbin/init"
mkdir -p "$root/fs/scratch" "$root/fs/run" "$root/fs/tmp" "$root/fs/sys/fs/cgroup" "$root/fs/dev" "$root/fs/proc"
ln -sf /run/resolv.conf "$root/fs/etc/resolv.conf"
# The selected base must supply sh, mount, ip, CA certificates, and rootless prerequisites.
for binary in bin/sh bin/mount usr/sbin/ip; do test -e "$root/fs/$binary"; done
truncate -s 2G "$OUTPUT"
mkfs.ext4 -q -F -d "$root/fs" "$OUTPUT"
chmod 444 "$OUTPUT"
sha256sum "$OUTPUT"
