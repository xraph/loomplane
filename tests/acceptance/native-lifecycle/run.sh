#!/usr/bin/env bash
set -euo pipefail

# Keep migration-heavy control-plane fixtures separate from the SQL instance
# whose reachable databases the recovery verifier inspects.
# Ephemeral metadata uses bounded tmpfs so parallel qualification disk writes
# cannot starve schema fixtures. This does not test host-loss durability.
root=$(cd "$(dirname "$0")/../../.." && pwd)
container="lp-native-lifecycle-control-$$"
trap 'docker rm -f "$container" >/dev/null' EXIT
docker run --rm -d --tmpfs /var/lib/postgresql/data:rw,size=1g --name "$container" -e POSTGRES_USER=loomplane -e POSTGRES_PASSWORD=loomplane -e POSTGRES_DB=postgres -p 127.0.0.1::5432 postgres:17-alpine >/dev/null
ready=false
for ((attempt=0; attempt<60; attempt++)); do
  if docker exec "$container" pg_isready -h 127.0.0.1 -U loomplane >/dev/null 2>&1; then
    ready=true
    break
  fi
  sleep 1
done
[[ "$ready" == true ]] || { echo 'private control-plane PostgreSQL did not become ready' >&2; exit 1; }
binding=$(docker port "$container" 5432/tcp)
port=${binding##*:}
export LOOMPLANE_TEST_DATABASE_URL="postgres://loomplane:loomplane@127.0.0.1:$port/postgres?sslmode=disable&connect_timeout=15&statement_timeout=45000"
export LOOMPLANE_TEST_NATIVE_TIMEOUT="${LOOMPLANE_TEST_NATIVE_TIMEOUT:-15m}"
cd "$root"
bash tests/acceptance/native-postgres/run.sh "${1:-'^TestNativeRetirement'}"
