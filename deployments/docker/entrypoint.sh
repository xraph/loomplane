#!/bin/sh
# Starts the API or the analysis worker, chosen by LOOMPLANE_PROCESS.
# Some platforms (Miget, for one) deploy each Compose service as an app and
# drop `command:` and `entrypoint:` overrides, so the worker can't be picked
# by entrypoint there. An environment variable reaches every platform.
#
# Any arguments go to the chosen binary, so `docker run IMAGE backup ...`
# still runs `loomplane backup ...`. `--entrypoint loomplane-worker` keeps
# working too: it bypasses this script.
set -eu

case "${LOOMPLANE_PROCESS:-api}" in
api) exec loomplane "$@" ;;
worker) exec loomplane-worker "$@" ;;
*)
	# Fail rather than guess: a typo here would otherwise start a second API
	# where you meant a worker, and nothing would work the analysis queue.
	echo "loomplane: LOOMPLANE_PROCESS must be api or worker, got \"$LOOMPLANE_PROCESS\"" >&2
	exit 64
	;;
esac
