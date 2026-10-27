#!/usr/bin/env bash
set -euo pipefail

# This private instance lets the SQL tests inspect every reachable database.
# Never point the bootstrap below at a shared server or a source database.
root=$(cd "$(dirname "$0")/../../.." && pwd)
container="lp-native-sql-verify-$$"
trap 'docker rm -f "$container" >/dev/null' EXIT
docker run --rm -d --name "$container" -e POSTGRES_USER=loomplane -e POSTGRES_PASSWORD=loomplane -e POSTGRES_DB=postgres -p 127.0.0.1::5432 postgres:17-alpine >/dev/null
ready=false
for ((attempt=0; attempt<60; attempt++)); do
  if docker exec "$container" pg_isready -h 127.0.0.1 -U loomplane >/dev/null 2>&1; then
    ready=true
    break
  fi
  sleep 1
done
[[ "$ready" == true ]] || { echo 'private PostgreSQL did not become ready' >&2; exit 1; }
# Positive fixtures represent a copy whose large-object creation capability has
# already been restricted. The verifier itself must never rewrite source ACLs.
for database in postgres template1; do
  docker exec "$container" psql -U loomplane -d "$database" -v ON_ERROR_STOP=1 -c 'REVOKE EXECUTE ON FUNCTION pg_catalog.lo_create(oid), pg_catalog.lo_creat(integer), pg_catalog.lo_from_bytea(oid,bytea) FROM PUBLIC' >/dev/null
done
binding=$(docker port "$container" 5432/tcp)
port=${binding##*:}
export LOOMPLANE_TEST_NATIVE_POSTGRES_URL="postgres://loomplane:loomplane@127.0.0.1:$port/postgres?sslmode=disable&connect_timeout=5&statement_timeout=15000"
cd "$root"
test_pattern=${1:-'^TestNative(PostgresVerification|CloudSQLCopyBackend)'}
GOMAXPROCS=${GOMAXPROCS:-4} go test -p 4 -tags=integration -race -timeout="${LOOMPLANE_TEST_NATIVE_TIMEOUT:-5m}" -count=1 -v -run "$test_pattern" ./tests/integration
