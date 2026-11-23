#!/bin/sh
# Builds a runner toolchain bundle: the Firecracker toolchain an on-demand
# runner installs before it joins, with a manifest of every file's sha256.
#
#   FIRECRACKER=/path/firecracker JAILER=/path/jailer KERNEL=/path/vmlinux \
#   ROOTFS=/path/guest.ext4 CNI_DIR=/opt/cni/bin ARCH=amd64 \
#   OUTPUT=/tmp/loomplane-runner-toolchain-amd64.tar.gz \
#     deployments/firecracker/build-runner-toolchain.sh
#
# Use the same reviewed set a Firecracker host runs: the Firecracker and
# jailer release, the guest kernel, the guest image build-guest.sh made, and
# the CNI ptp, host-local and tc-redirect-tap plugins. Publish the bundle at
# an https URL and put that URL and the printed sha256 in the pool's
# toolchain_url and toolchain_sha256. Runners check the sha256 before they
# unpack anything, then every file against the manifest.
set -eu

for v in FIRECRACKER JAILER KERNEL ROOTFS CNI_DIR ARCH OUTPUT; do
	eval "val=\${$v:-}"
	[ -n "$val" ] || { echo "set $v" >&2; exit 2; }
done
case "$ARCH" in amd64 | arm64) ;; *) echo "ARCH is amd64 or arm64" >&2; exit 2 ;; esac
case "$OUTPUT" in /*) ;; *) echo "OUTPUT must be an absolute path" >&2; exit 2 ;; esac
[ ! -e "$OUTPUT" ] || { echo "$OUTPUT exists" >&2; exit 2; }

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT HUP INT TERM
mkdir -p "$work/bin" "$work/cni"
install -m 0755 "$FIRECRACKER" "$work/bin/firecracker"
install -m 0755 "$JAILER" "$work/bin/jailer"
install -m 0644 "$KERNEL" "$work/vmlinux"
install -m 0644 "$ROOTFS" "$work/rootfs.ext4"
for p in ptp host-local tc-redirect-tap; do
	install -m 0755 "$CNI_DIR/$p" "$work/cni/$p"
done

sum() { sha256sum "$1" | cut -d' ' -f1; }
entry() { printf '"%s": {"path": "%s", "sha256": "%s"}' "$1" "$2" "$(sum "$work/$2")"; }
{
	printf '{"arch": "%s", "files": {' "$ARCH"
	entry firecracker bin/firecracker
	printf ', '
	entry jailer bin/jailer
	printf ', '
	entry kernel vmlinux
	printf ', '
	entry rootfs rootfs.ext4
	printf ', '
	entry cni/ptp cni/ptp
	printf ', '
	entry cni/host-local cni/host-local
	printf ', '
	entry cni/tc-redirect-tap cni/tc-redirect-tap
	printf '}}\n'
} >"$work/manifest.json"

tar -C "$work" --owner=0 --group=0 -czf "$OUTPUT" manifest.json bin cni vmlinux rootfs.ext4
echo "bundle:  $OUTPUT"
echo "sha256:  $(sum "$OUTPUT")"
