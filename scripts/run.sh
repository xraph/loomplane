#!/usr/bin/env bash
# Runs the built Loomplane API and analysis worker side by side. Invoked by
# `make run`, which builds bin/ first and sets API_PORT. The worker starts
# once the API answers, because the API migrates the database it uses.

set -u
set -m

prefix() { awk -v p="$1" '{ print p $0; fflush() }'; }

LOOMPLANE_HTTP_ADDR=":${API_PORT}" ./bin/loomplane 2>&1 | prefix $'\033[0;36m[api]\033[0m ' &
API_PID=$!

(
	until curl -fsS -o /dev/null "http://localhost:${API_PORT}/_/health/live" 2>/dev/null; do sleep 1; done
	exec ./bin/loomplane-worker
) 2>&1 | prefix $'\033[0;33m[worker]\033[0m ' &
WORKER_PID=$!

stopping=0
stop_all() {
	[ "$stopping" = 1 ] && return
	stopping=1
	kill -TERM -"$API_PID" -"$WORKER_PID" 2>/dev/null
	sleep 2
	kill -KILL -"$API_PID" -"$WORKER_PID" 2>/dev/null
}
trap 'stop_all; exit 130' INT TERM
trap 'stop_all' EXIT

echo "loomplane: http://localhost:${API_PORT}"

while kill -0 "$API_PID" 2>/dev/null && kill -0 "$WORKER_PID" 2>/dev/null; do
	sleep 1
done
