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
refuses "deploying into this cluster with no namespace to deploy into" --set publicURL=https://x.example \
	--set secrets.secretKey="$key" --set secrets.databaseURL=postgres://x --set storage.url=s3://b \
	--set inCluster.enabled=true
refuses "deploying into Loomplane's own namespace" --namespace loomplane --set publicURL=https://x.example \
	--set secrets.secretKey="$key" --set secrets.databaseURL=postgres://x --set storage.url=s3://b \
	--set inCluster.enabled=true --set 'inCluster.namespaces={loomplane}'
refuses "a waitlist that isn't true, false or empty" --set publicURL=https://x.example \
	--set secrets.secretKey="$key" --set secrets.databaseURL=postgres://x --set storage.url=s3://b \
	--set waitlist=sometimes

# Enabling the gateway must create a separate workload and public route.
helm template loomplane "$chart" -f "$chart/ci/push-gateway-values.yaml" \
  --show-only templates/push-gateway.yaml | grep -q 'name: push-gateway'
gateway_args=(-f "$chart/ci/push-gateway-values.yaml")
refuses "a gateway without a Secret" "${gateway_args[@]}" --set push.gateway.existingSecret=
refuses "a gateway without a sealing key ID" "${gateway_args[@]}" --set push.gateway.sealingKeyId=
refuses "a gateway without an image version" "${gateway_args[@]}" --set push.gateway.image.tag=
refuses "a gateway without a push platform" "${gateway_args[@]}" --set push.gateway.fcm.enabled=false
refuses "a gateway with incomplete APNs settings" "${gateway_args[@]}" --set push.gateway.apns.enabled=true
refuses "a gateway with a plain HTTP address" "${gateway_args[@]}" --set push.gatewayUrl=http://push.example.com
refuses "a gateway sharing the dashboard hostname" "${gateway_args[@]}" --set push.gatewayUrl=https://loomplane.example.com
refuses "a gateway ingress without TLS" "${gateway_args[@]}" --set-json 'push.gateway.ingress.tls=[]'
refuses "a gateway route without a parent" "${gateway_args[@]}" --set-json 'push.gateway.httpRoute.parentRefs=[]'
refuses "a gateway reusing the current sealing key ID" "${gateway_args[@]}" --set push.gateway.previousSealingKeyId=ci

# An iOS-only gateway must not require a Firebase key, and rotation mounts
# the old key only while its ID is configured.
ios=$(helm template loomplane "$chart" "${gateway_args[@]}" \
  --set push.gateway.fcm.enabled=false --set push.gateway.apns.enabled=true \
  --set push.gateway.apns.keyId=ABCDE12345 --set push.gateway.apns.teamId=TEAM123456 \
  --set push.gateway.previousSealingKeyId=previous \
  --show-only templates/push-gateway.yaml)
grep -q 'key: apns-key.p8' <<<"$ios"
grep -q 'key: previous-sealing-key' <<<"$ios"
if grep -Eq 'fcm-service-account|envFrom:|LOOMPLANE_DATABASE_URL|LOOMPLANE_SECRET_KEY' <<<"$ios"; then
  echo "The iOS gateway received unrelated credentials" >&2
  exit 1
fi

rendered=$(mktemp)
trap 'rm -f "$rendered"' EXIT
bash scripts/render-k8s-manifest.sh >"$rendered"
if ! diff -u deployments/kubernetes/loomplane.yaml "$rendered"; then
	echo "deployments/kubernetes/loomplane.yaml is stale. Regenerate it with: make k8s-manifest" >&2
	exit 1
fi
echo "The chart is valid and the plain manifest is current."
