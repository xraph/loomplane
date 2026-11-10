#!/bin/sh
# Runs after the loomplane package unpacks, on install and on upgrade.
#
# It never starts or restarts a service. An upgrade has an order (stop both,
# start the API so it migrates, then the worker), so you run it yourself.
set -e

# Secrets go in here, so only root and the services may read it.
chown root:loomplane /etc/loomplane/loomplane.env
chmod 0640 /etc/loomplane/loomplane.env

if [ -d /run/systemd/system ] && command -v systemctl >/dev/null 2>&1; then
	systemctl daemon-reload >/dev/null 2>&1 || true
fi

# dpkg passes "configure" with the old version on upgrade; rpm passes 2.
if [ "$1" = configure ] && [ -z "${2:-}" ] || [ "$1" = 1 ]; then
	cat <<'MSG'
Loomplane is installed. Next:
  1. Fill in /etc/loomplane/loomplane.env (secret key, database URL, public URL).
  2. sudo systemctl enable --now loomplane loomplane-worker
https://www.loomplane.com/docs/operations/linux has the rest.
MSG
else
	cat <<'MSG'
Loomplane was upgraded. Its services were left as they were. To run the new
version: sudo systemctl restart loomplane, wait for it to answer, then
sudo systemctl restart loomplane-worker.
MSG
fi
