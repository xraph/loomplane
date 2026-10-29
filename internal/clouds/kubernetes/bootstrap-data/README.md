# Kubernetes setup bundles

These manifests come from cert-manager v1.21.3, Traefik Helm chart 42.1.3, local-path-provisioner v0.0.37, Longhorn chart 1.13.0 and MetalLB chart 0.16.1. You can reproduce them with Helm 3 or later. Loomplane reads the embedded YAML and uses the Kubernetes API directly; it does not create Helm releases or run Helm in production.

Sources:

- https://github.com/cert-manager/cert-manager/tree/v1.21.3/deploy/charts/cert-manager
- https://github.com/traefik/traefik-helm-chart/tree/v42.1.3/traefik
- https://github.com/rancher/local-path-provisioner/tree/v0.0.37
- https://github.com/longhorn/charts/tree/longhorn-1.13.0
- https://github.com/metallb/metallb/tree/v0.16.1/charts/metallb

The corresponding licenses are included in this directory. Keep them with redistributed manifests. Controller images are pinned to multi-platform digests. Bump its entry in `bootstrapVersions` whenever you change a bundle. Before updating a bundle, review upstream release and migration notes, verify its image digests, and test both installation and coexistence with an external installation. Setup refuses a different owned bundle version. The maintenance upgrade action applies compatible patch releases within the same upstream major and minor version, preserving Service allocations, Secret data and injected webhook CA bundles. Larger migrations need a reviewed implementation.

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

Render the optional storage and network bundles with:

```sh
helm template loomplane-longhorn longhorn --repo https://charts.longhorn.io \
  --version 1.13.0 --namespace loomplane-system --kube-version 1.36.0 --include-crds \
  --set persistence.defaultClass=false --set persistence.reclaimPolicy=Retain \
  --set persistence.defaultClassReplicaCount=3 --set defaultSettings.defaultReplicaCount=3 \
  --set preUpgradeChecker.jobEnabled=false

helm template loomplane-metallb metallb --repo https://metallb.github.io/metallb \
  --version 0.16.1 --namespace loomplane-system --include-crds \
  --set frrk8s.enabled=false --set speaker.frr.enabled=false
```

Keep Longhorn's post-upgrade and uninstall Jobs in their separate files. They run only through the corresponding maintenance action. Remove `--force` from the uninstall command. Its volume dependency checks must pass before the deletion confirmation setting is enabled. The Longhorn chart digest is `sha256:e57f010d2a8bcf0da5b8b03ffecf49c5bc35ddc2663588ce8f22b7567b6133ed`; MetalLB's is `sha256:fb06bb584fcb7856f15733b2a6a2aff5b61b5c350687e341c163ae24a5938adc`.

For local-path, start with the release's `deploy/local-path-storage.yaml`. Move it to `loomplane-system`, prefix resource names with `loomplane-`, use `/opt/loomplane-local-path`, and create `lp-local-retained` with a Retain policy. Pass the matching configmap name, service account, provisioner name (`loomplane.io/local-path`) and pinned helper image explicitly. Pin every controller, helper and storage engine image to its multi-platform digest after rendering.
