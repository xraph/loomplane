# Offer the Loomplane wizard on the first interactive root login. It stays out
# of the way of scp, sftp and `ssh host command`, which have no terminal.
case $- in
  *i*)
    if [ "$(id -u)" -eq 0 ] && [ -t 0 ] && [ -t 1 ] \
      && [ -f /var/lib/cloud/instance/boot-finished ] \
      && [ ! -f "${LOOMPLANE_DIR:-/srv/loomplane}/.configured" ] \
      && [ -x /usr/local/bin/loomplane-setup ]; then
      /usr/local/bin/loomplane-setup || true
    fi
    ;;
esac
