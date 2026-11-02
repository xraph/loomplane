#!/usr/bin/env bash
# Prints the plain Kubernetes manifest: the Helm chart rendered with
# deployments/kubernetes/values.yaml. make k8s-manifest writes it to
# deployments/kubernetes/loomplane.yaml. Extra arguments go to helm template,
# so a release can render its own: --set image.tag=v1.2.3
set -euo pipefail

cat <<'HEADER'
# Loomplane for kubectl apply, rendered from deployments/helm/loomplane with
# deployments/kubernetes/values.yaml. Don't edit it by hand: change those and
# run make k8s-manifest. The Helm chart is the better way to run Loomplane;
# this is for clusters without Helm. See docs/kubernetes.md.
#
# Before you apply it:
#   1. Create the namespace and the Secret it reads:
#        kubectl create namespace loomplane
#        kubectl -n loomplane create secret generic loomplane \
#          --from-literal=LOOMPLANE_SECRET_KEY="$(openssl rand -base64 32)" \
#          --from-literal=LOOMPLANE_DATABASE_URL='postgres://...' \
#          --from-literal=LOOMPLANE_STORAGE_URL='s3://REGION/BUCKET'
#   2. Replace loomplane.example.com with your hostname, and pin the image
#      to a release tag instead of main.
#   3. kubectl apply -f loomplane.yaml
HEADER
helm template loomplane deployments/helm/loomplane --namespace loomplane --skip-tests \
	-f deployments/kubernetes/values.yaml "$@"
