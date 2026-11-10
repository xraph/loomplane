#!/bin/sh
# Runs before the loomplane package is removed or replaced. Stops the
# services only when the package is going away, not on an upgrade.
set -e

case "$1" in
remove | purge | 0)
	if [ -d /run/systemd/system ] && command -v systemctl >/dev/null 2>&1; then
		systemctl disable --now loomplane-worker.service loomplane.service >/dev/null 2>&1 || true
	fi
	;;
esac
