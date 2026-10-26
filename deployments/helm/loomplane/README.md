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
