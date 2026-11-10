#!/bin/sh
# Runs after the loomplane package is removed. The loomplane user,
# /var/lib/loomplane and /var/lib/loomplane-work stay, because they hold your
# data. The docs say how to remove them once you're sure.
set -e

if [ -d /run/systemd/system ] && command -v systemctl >/dev/null 2>&1; then
	systemctl daemon-reload >/dev/null 2>&1 || true
fi
