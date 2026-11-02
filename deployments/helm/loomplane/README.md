# Loomplane Helm chart

Runs [Loomplane](https://github.com/xraph/loomplane) highly available: two
API replicas and two analysis workers, spread across nodes and zones, with
disruption budgets, rolling updates and migrations that run once before any
new pod serves. You bring PostgreSQL 17 and S3-compatible object storage.

```bash
kubectl create namespace loomplane
kubectl -n loomplane create secret generic loomplane \
  --from-literal=LOOMPLANE_SECRET_KEY="$(openssl rand -base64 32)" \
  --from-literal=LOOMPLANE_DATABASE_URL='postgres://loomplane:PASSWORD@db:5432/loomplane?sslmode=verify-full' \
  --from-literal=LOOMPLANE_STORAGE_URL='s3://us-east-1/loomplane-artifacts'

helm install loomplane oci://ghcr.io/xraph/charts/loomplane --version VERSION \
  --namespace loomplane \
  --set publicURL=https://loomplane.example.com \
  --set secrets.existingSecret=loomplane \
  --set ingress.enabled=true --set ingress.className=nginx

helm test loomplane -n loomplane
```

Create the `loomplane-artifacts` and `loomplane-backups` buckets first. The
chart refuses more than one replica on each pod's own disk, since the pods
have to share files.

[docs/kubernetes.md](https://github.com/xraph/loomplane/blob/main/docs/kubernetes.md)
covers what you need, upgrades and rollbacks, what survives which failure,
and the settings worth knowing. `values.yaml` documents every value.

To run the push gateway in the same release, set `push.gateway.enabled=true`,
`push.gatewayUrl` to a separate HTTPS hostname, and supply its image version,
sealing key ID and `push.gateway.existingSecret`. The gateway mounts its own
keys separately from Loomplane's credentials. Enable its Ingress or HTTPRoute
when you want the chart to publish the two push routes. The
[push notifications guide](https://loomplane.dev/docs/operations/push-notifications#alongside-loomplane)
covers secret files, TLS and key rotation. You need push credentials for the
mobile app you ship; the store app uses XRaph's gateway.
