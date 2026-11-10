#!/bin/sh
# Runs before the loomplane package unpacks, on install and on upgrade.
# Creates the loomplane system user the services run as.
set -e

if ! getent group loomplane >/dev/null 2>&1; then
	groupadd --system loomplane
fi
if ! getent passwd loomplane >/dev/null 2>&1; then
	nologin=/usr/sbin/nologin
	[ -x "$nologin" ] || nologin=/sbin/nologin
	useradd --system --gid loomplane --no-create-home \
		--home-dir /var/lib/loomplane --shell "$nologin" \
		--comment "Loomplane" loomplane
fi
