#!/usr/bin/env bash
set -euo pipefail
root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
image=${1:-loomplane-duckdb:local}
mode=${2:---load}
if [[ $# -gt 2 || ( "$mode" != "--load" && "$mode" != "--push" ) ]]; then
  echo "Usage: scripts/build-duckdb-runtime.sh [repository:tag] [--load|--push]" >&2
  exit 2
fi
if [[ "$mode" == "--push" && $# -lt 1 ]]; then
  echo "Choose the destination image before publishing." >&2
  exit 2
fi
exec docker buildx build --platform linux/amd64,linux/arm64 \
  --file "$root/deployments/duckdb/Dockerfile" --tag "$image" "$mode" "$root"
