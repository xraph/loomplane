#!/usr/bin/env bash
# Lints the Helm chart, validates everything it renders against the
# Kubernetes schemas, checks it refuses settings that can't be highly
# available, and checks deployments/kubernetes/loomplane.yaml is what the
# chart renders today (make k8s-manifest rewrites it).
#
# Needs helm and kubeconform.
set -euo pipefail

chart=deployments/helm/loomplane
kube_version=${KUBE_VERSION:-1.30.0}
# ServiceMonitor and HTTPRoute come from CRDs; their schemas live here.
crds='https://raw.githubusercontent.com/datreeio/CRDs-catalog/main/{{.Group}}/{{.ResourceKind}}_{{.ResourceAPIVersion}}.json'

for values in "$chart"/ci/*.yaml deployments/kubernetes/values.yaml; do
	echo "== $values"
	helm lint "$chart" --strict -f "$values"
	helm template loomplane "$chart" --namespace loomplane -f "$values" |
		kubeconform -strict -summary -kubernetes-version "$kube_version" \
			-schema-location default -schema-location "$crds"
done

refuses() {
	local why=$1
	shift
	if helm template loomplane "$chart" "$@" >/dev/null 2>&1; then
		echo "the chart rendered when it should refuse: $why" >&2
		exit 1
	fi
}
refuses "no publicURL"
refuses "no secret key" --set publicURL=https://x.example
refuses "a secret key that isn't 32 bytes" --set publicURL=https://x.example \
	--set secrets.secretKey=dG9vIHNob3J0 --set secrets.databaseURL=postgres://x
key=$(printf '%032d' 0 | base64)
refuses "two replicas on local files" --set publicURL=https://x.example \
	--set secrets.secretKey="$key" --set secrets.databaseURL=postgres://x
refuses "a shared volume that isn't ReadWriteMany" --set publicURL=https://x.example \
	--set secrets.secretKey="$key" --set secrets.databaseURL=postgres://x \
	--set storage.persistence.enabled=true --set 'storage.persistence.accessModes={ReadWriteOnce}'

rendered=$(mktemp)
trap 'rm -f "$rendered"' EXIT
bash scripts/render-k8s-manifest.sh >"$rendered"
if ! diff -u deployments/kubernetes/loomplane.yaml "$rendered"; then
	echo "deployments/kubernetes/loomplane.yaml is stale. Regenerate it with: make k8s-manifest" >&2
	exit 1
fi
echo "The chart is valid and the plain manifest is current."
