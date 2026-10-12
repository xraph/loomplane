#!/usr/bin/env bash
# Runs the Loomplane API (forge dev), the analysis worker and the dashboard
# (Vite) side by side. Invoked by `make dev`, which sets API_PORT and
# DASHBOARD_DIR. The worker starts once the API answers, because the API
# migrates the database the worker uses. It doesn't reload on changes;
# stop and start `make dev` after editing a detector.
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

(
	until curl -fsS -o /dev/null "http://localhost:${API_PORT}/_/health/live" 2>/dev/null; do sleep 1; done
	exec go run ./cmd/loomplane-worker
) 2>&1 | prefix $'\033[0;33m[worker]\033[0m ' &
WORKER_PID=$!

stopping=0
stop_all() {
	[ "$stopping" = 1 ] && return
	stopping=1
	kill -TERM -"$API_PID" -"$WEB_PID" -"$WORKER_PID" 2>/dev/null
	sleep 2
	kill -KILL -"$API_PID" -"$WEB_PID" -"$WORKER_PID" 2>/dev/null
}
trap 'stop_all; exit 130' INT TERM
trap 'stop_all' EXIT

echo "api:       http://localhost:${API_PORT}"
echo "dashboard: http://localhost:5173 (proxies the API)"

# When any of them exits, stop the rest: a dashboard without its API, or an
# API you can't see, is rarely what you want running.
while kill -0 "$API_PID" 2>/dev/null && kill -0 "$WEB_PID" 2>/dev/null && kill -0 "$WORKER_PID" 2>/dev/null; do
	sleep 1
done
