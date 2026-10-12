#!/usr/bin/env bash
# Runs the Loomplane API (forge dev) and the dashboard (Vite) side by side.
# Invoked by `make dev`, which sets API_PORT and DASHBOARD_DIR.
#
# macOS ships bash 3.2 (no `wait -n`), so the children are polled. `set -m`
# puts each in its own process group, so one signal per side stops forge's
# rebuilt binary and Vite's server too. The shape is tap's scripts/dev.sh.

set -u
set -m

prefix() { awk -v p="$1" '{ print p $0; fflush() }'; }

forge dev -a loomplane -p "$API_PORT" 2>&1 | prefix $'\033[0;36m[api]\033[0m ' &
API_PID=$!

LOOMPLANE_API_URL="http://localhost:${API_PORT}" \
	pnpm --dir "$DASHBOARD_DIR" dev 2>&1 | prefix $'\033[0;35m[web]\033[0m ' &
WEB_PID=$!

stopping=0
stop_all() {
	[ "$stopping" = 1 ] && return
	stopping=1
	kill -TERM -"$API_PID" -"$WEB_PID" 2>/dev/null
	sleep 2
	kill -KILL -"$API_PID" -"$WEB_PID" 2>/dev/null
}
trap 'stop_all; exit 130' INT TERM
trap 'stop_all' EXIT

echo "api:       http://localhost:${API_PORT}"
echo "dashboard: http://localhost:5173 (proxies the API)"

# When either side exits, stop the other: a dashboard without its API, or an
# API you can't see, is rarely what you want running.
while kill -0 "$API_PID" 2>/dev/null && kill -0 "$WEB_PID" 2>/dev/null; do
	sleep 1
done
