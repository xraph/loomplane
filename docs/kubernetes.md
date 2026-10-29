# Run Loomplane highly available on Kubernetes

The Helm chart in `deployments/helm/loomplane` runs Loomplane with no single
pod you can't lose. You get two API replicas and two analysis workers, spread
across nodes and zones, with disruption budgets, rolling updates that never
drop below the replicas you asked for, and migrations that run once, before
any new pod serves. You bring PostgreSQL and S3-compatible object storage.

For one host without Kubernetes, use the [Docker guide](docker-production.md)
instead. It's simpler, and it isn't highly available.

## What you need

- Kubernetes 1.27 or newer, with nodes in at least two zones if you want to
  survive losing a zone.
- Helm 3.8 or newer, or Helm 4. The chart comes from an OCI registry.
- PostgreSQL 17, the version Loomplane is tested on, reachable from the
  cluster. Use a managed database or a
  replicated one such as CloudNativePG: the database is the one piece this
  chart doesn't make highly available, so it decides how available
  Loomplane is.
- Object storage that speaks S3: AWS S3, GCS through its S3 interoperability
  API, Cloudflare R2, DigitalOcean Spaces, MinIO. Create two buckets first,
  `loomplane-artifacts` and `loomplane-backups` (the names are settings).
  A ReadWriteMany volume such as EFS or Filestore works too.
- An ingress controller or a Gateway API implementation, and a hostname.

## Install

Every release publishes the chart next to its images:

```bash
helm install loomplane oci://ghcr.io/xraph/charts/loomplane --version 1.2.3 \
  --namespace loomplane --create-namespace -f values.yaml
```

From a checkout, `deployments/helm/loomplane` installs the same way and runs
the `main` image CI publishes for every commit. Pin a release tag for
anything that matters.

Create the Secret first, so no credential ever sits in a values file:

```bash
kubectl create namespace loomplane
kubectl -n loomplane create secret generic loomplane \
  --from-literal=LOOMPLANE_SECRET_KEY="$(openssl rand -base64 32)" \
  --from-literal=LOOMPLANE_DATABASE_URL='postgres://loomplane:PASSWORD@db.internal:5432/loomplane?sslmode=verify-full' \
  --from-literal=LOOMPLANE_STORAGE_URL='s3://us-east-1/loomplane-artifacts'
```

Keep that secret key in your secret manager. Loomplane derives the keys that
encrypt stored credentials from it, and a database backup is useless without
the key it ran with. Generate it once.

Then a `values.yaml` like this one:

```yaml
publicURL: https://loomplane.example.com
secrets:
  existingSecret: loomplane
serviceAccount:
  # Lets an S3 URL without keys use the pod's cloud identity.
  annotations:
    eks.amazonaws.com/role-arn: arn:aws:iam::123456789012:role/loomplane
ingress:
  enabled: true
  className: nginx
  annotations:
    nginx.ingress.kubernetes.io/proxy-buffering: "off"
    nginx.ingress.kubernetes.io/proxy-read-timeout: "3600"
    nginx.ingress.kubernetes.io/proxy-send-timeout: "3600"
    nginx.ingress.kubernetes.io/proxy-body-size: 64m
    cert-manager.io/cluster-issuer: letsencrypt
  tls:
    - secretName: loomplane-tls
      hosts: [loomplane.example.com]
```

Follow the rollout, then run the chart's test, which asks a ready pod through
the Service:

```bash
kubectl -n loomplane rollout status deploy/loomplane
kubectl -n loomplane rollout status deploy/loomplane-worker
helm test loomplane -n loomplane
```

Open your hostname and create the first account. It owns the instance.

Without the Secret, the chart can make one from `secrets.secretKey`,
`secrets.databaseURL` and `storage.url`. That's handy for a trial and worse
for production, because the values end up in Helm's release history.

## What survives what

| Event | What happens |
|---|---|
| An API pod crashes or is deleted | The Service stops sending it traffic; the other replica serves. Its replacement migrates nothing and joins in seconds. |
| A worker pod dies mid-analysis | The job's lease runs out after 30 seconds and another worker picks it up. It doesn't count as a retry. |
| A node drains for maintenance | The disruption budgets let one API and one worker pod go at a time, and each is rescheduled before the next leaves. |
| A zone fails | With nodes in two zones, the spread constraints put a replica in each. |
| An upgrade | New pods migrate first, under a lock, then replace old ones one at a time. A failed migration stops the rollout and the old pods keep serving. |
| PostgreSQL is down | Loomplane is down. Make the database highly available. |
| Object storage is down | Deployments that need source archives and backups fail until it's back. Everything else works. |

`make test-kubernetes` proves the first four on a three-node kind cluster:
it keeps requests going through the Service while it deletes an API pod,
restarts both Deployments and drains a node, and fails on a single error.
CI runs it on every push.

## How it stays consistent with several replicas

Everything replicas share lives in PostgreSQL, so no pod holds state another
one needs.

- **Migrations.** Each API pod runs `loomplane migrate` in an init container.
  A Postgres advisory lock lets one pod migrate while the others wait and
  then find nothing left. Workers never migrate: their init container waits
  until `loomplane migrations --check` reports the schema current.
- **Background work.** Deployments, analyses, drift checks and backups run as
  Dispatch jobs, which Postgres hands to one worker at a time with leases.
  Ctrl Plane's scheduler runs on one API pod, elected through an advisory
  lock; when that pod goes, another takes the lock within a minute. First
  start seeding (the authsome app, mail templates, the SMTP provider) takes
  a lock too, so replicas that boot together don't create things twice.
- **Sessions and live events.** Sessions, rate limits and sign-in state are
  in Postgres. Live events are stored there and announced with
  `pg_notify`, so a browser connected to one pod sees events another pod
  produced. You don't need sticky sessions.
- **Files.** Source archives and backups go to object storage, which every
  pod reads. The chart refuses to render more than one replica on each pod's
  own disk.

## Upgrades and rollbacks

```bash
helm upgrade loomplane oci://ghcr.io/xraph/charts/loomplane --version 1.2.4 \
  -n loomplane -f values.yaml
```

Take a backup first ([Backups](operations.md#backups)).
Migrations only go forward, and an older Loomplane refuses a database a newer
one migrated. So `helm rollback` after an upgrade that migrated won't start;
roll back by restoring the backup you took and installing the older version.

Old pods keep serving while new ones roll out, on the schema the new version
just migrated. That's safe for migrations that add things, which is how
Loomplane writes them. If a release's notes say it needs downtime, set
`api.strategy.type` and `worker.strategy.type` to `Recreate` for that
upgrade.

## Settings worth knowing

| Value | Default | What it does |
|---|---|---|
| `api.replicaCount`, `worker.replicaCount` | `2` | Replicas when autoscaling is off |
| `api.autoscaling.enabled` | `false` | CPU-based scaling between `minReplicas` and `maxReplicas` |
| `api.podDisruptionBudget.minAvailable` | `1` | Pods a drain must leave running |
| `storage.url` | | `s3://REGION/BUCKET`, with `KEY:SECRET@` and `?endpoint=...&path_style=true` for S3-compatible stores |
| `storage.persistence.enabled` | `false` | Files on a ReadWriteMany claim instead of S3 |
| `worker.tmpSize` | `8Gi` | Scratch space for checkouts: one analysis can unpack 2 GiB and a worker runs two |
| `database.poolSize` | `16` | Connections each pod keeps open |
| `configFile` | | `config.local.yaml` for settings only config carries, like provider plugins |
| `trustedProxies` | private ranges | Proxies whose `X-Forwarded-For` sign-in rate limits believe |
| `waitlist` | | `true` or `false` forces the sign-up waitlist on or off; empty leaves it to the instance ([Running Loomplane](operations.md#who-can-sign-up)) |
| `consolePath` | `/__console` | Where platform admins open the console ([Running Loomplane](operations.md#the-console)) |
| `ingress.blockInternalPaths` | `true` | Sends `/_/health`, `/_/metrics` and `/debug` to a Service with no pods |
| `metrics.serviceMonitor.enabled` | `false` | Prometheus Operator scraping for both processes |
| `extraVolumes`, `extraVolumeMounts` | | The SPIRE socket and signing keys for [federation](federation-agent.md) |
| `inCluster.enabled` | `false` | Lets people deploy into this cluster with no kubeconfig ([below](#deploying-into-the-same-cluster)) |

`values.yaml` documents the rest.

Each API pod opens a little over 20 database connections (its pool, the
event listener and the Ctrl Plane leader) and each worker about 16. Size
PostgreSQL's `max_connections` for your replica count plus headroom for a
rolling update's extra pod, or put PgBouncer in session mode in front.

Each API pod also runs about two dozen job pollers on its pool. With the
database a millisecond away that's plenty. If the API logs `begin tx:
context deadline exceeded` and goes unready, the pool is the bottleneck:
raise `database.poolSize` and `max_connections` together.

## Deploying into the same cluster

You can let people deploy to the cluster Loomplane itself runs in, with no
kubeconfig to paste. List the namespaces it may use:

```yaml
inCluster:
  enabled: true
  namespaces: [apps, preview]
  createNamespaces: true # leave it false when you make them yourself
```

The chart then mounts the service account token in the API pods (the
workers never call the cluster, so they still get none), sets
`LOOMPLANE_KUBERNETES_IN_CLUSTER`, and binds a Role in each namespace that
grants exactly what a Kubernetes connection's check asks for. Under
Integrations, the Kubernetes dialog now opens on this cluster. You pick a
namespace and connect.

Think about who's on your instance before you turn it on. Loomplane deploys
there as its own service account, and every organization shares it, so any
of them can deploy into any namespace you list. On a single-team install
that's exactly what you want. On a shared one, leave it off and give each
team a kubeconfig scoped to its own namespace.

Loomplane's own namespace is never a target, because its database password
and secret key live there. The chart won't render with it in the list, and
the API refuses it as well, along with `kube-system`, `kube-public` and
`kube-node-lease`.

`inCluster.clusterPermissions`, on by default, adds a ClusterRole for the
few things that aren't namespaced: listing nodes for GPU inventory, reading
storage classes, and reading and updating persistent volumes so a
database's volume survives recovery. Turn it off and those show as
unavailable on the connection. Plain deployments still work.

Loomplane keeps no copy of the token. Every connection reads the file the
kubelet mounted, so rotation needs nothing from you. Without Helm, set
`LOOMPLANE_KUBERNETES_IN_CLUSTER=true` on the API, set
`automountServiceAccountToken: true` on its pods, and grant its service
account what `templates/in-cluster-rbac.yaml` grants. If the token isn't
there, the API logs a warning at start and leaves the option off.

## Probes and shutdown

| Process | Port | Liveness | Readiness |
|---|---|---|---|
| API and dashboard | 8780 | `/_/health/live` | `/_/health/ready` |
| Worker | 8781 | `/_/health/live` | `/_/health/ready` |

Loomplane answers ready for its first 60 seconds whatever its health, so the
readiness probe starts at 65 seconds. A rollout then only retires an old pod
once the new one has passed a real check, which makes each pod take about a
minute to join.

Both serve Prometheus metrics on `/_/metrics`. On shutdown the API pod sleeps
10 seconds in `preStop` while its endpoint leaves every proxy, then gets 30
seconds to finish requests. A worker's unfinished jobs go back on the queue.

## Known limits

- A provider plugin that stops answering marks every API pod not ready at
  once, because readiness includes plugin health. If you run plugins, watch
  them as closely as the database.
- The rate limit on endpoint checks is kept per pod, so with N API replicas
  the effective limit is N times the setting.
- The chart doesn't install PostgreSQL or object storage, on purpose.

## Without Helm

`deployments/kubernetes/loomplane.yaml` is the chart rendered for
`kubectl apply`, with an ingress-nginx Ingress and a Secret you create. Its
header lists what to change. `make k8s-manifest` regenerates it after a chart
change, and CI fails when it's stale. Each release attaches the same file
pinned to that release's image.
