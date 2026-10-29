# Kubernetes setup bundles

These manifests come from the official cert-manager v1.21.3 and Traefik Helm chart 42.1.3 releases. You can reproduce them with Helm 3 or later. Loomplane reads the embedded YAML and uses the Kubernetes API directly; it does not create Helm releases or run Helm in production.

Sources:

- https://github.com/cert-manager/cert-manager/tree/v1.21.3/deploy/charts/cert-manager
- https://github.com/traefik/traefik-helm-chart/tree/v42.1.3/traefik

The corresponding licenses are included in this directory. Keep them with redistributed manifests. Controller images are pinned to multi-platform digests. Bump its entry in `bootstrapVersions` whenever you change a bundle. Before updating a bundle, review upstream release and migration notes, verify its image digests, and test both installation and coexistence with an external installation. Setup refuses a different owned bundle version; it does not perform controller or CRD upgrades.

```sh
helm template loomplane-cert-manager oci://quay.io/jetstack/charts/cert-manager \
  --version v1.21.3 --namespace loomplane-system --include-crds \
  --set crds.enabled=true --set crds.keep=true \
  --set startupapicheck.enabled=false --set prometheus.enabled=false \
  --set global.leaderElection.namespace=loomplane-system

helm template loomplane-ingress oci://ghcr.io/traefik/helm/traefik \
  --version 42.1.3 --namespace loomplane-system --skip-crds \
  --set providers.kubernetesCRD.enabled=false \
  --set ingressClass.name=loomplane --set ingressClass.isDefaultClass=false \
  --set providers.kubernetesIngress.ingressClass=loomplane \
  --set fullnameOverride=loomplane-ingress --set service.spec.type=LoadBalancer \
  --set ports.web.http.redirections.entryPoint.to=websecure \
  --set ports.web.http.redirections.entryPoint.scheme=https \
  --set ports.web.http.redirections.entryPoint.priority=1
```

After rendering, replace the four tagged controller image references with their verified `tag@sha256:digest` references. The chart digests used here are `sha256:e482675aec32862d323a9218293ff7de81192337998615e4378c56498473f240` (cert-manager) and `sha256:49458ccd9673cd828ee0b091030f5747f5d95f205bbd0cf0b6c3bf757398cae1` (Traefik).

The installer adds its cluster ownership label and bundle version annotation, and removes the Helm managed-by label. It only creates absent resources after preflight. It preserves webhook CA injection, allocated Service addresses and other controller-maintained fields by leaving existing owned resources untouched.
