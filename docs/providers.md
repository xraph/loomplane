# Writing a provider

A provider is a deployment target: somewhere Loomplane can run a service or provision a resource. Loomplane ships with Cloud Run, Vercel, ECS on Fargate, Render, DigitalOcean App Platform, Azure Container Apps, SSH/Docker servers and Kubernetes for services, and Cloud SQL, RDS, Memorystore, ElastiCache, Cloud Storage, S3, DigitalOcean Managed Databases, Azure Database for PostgreSQL and MySQL, and Azure Managed Redis for resources. The [DigitalOcean guide](digitalocean.md) covers how App Platform deploys an approved commit when it can only build a branch, and the [Azure guide](azure.md) covers the service principal Container Apps deploys with and the databases it makes. You can add your own without changing Loomplane: write a provider plugin, run it, and point Loomplane at it.

## Your servers and clusters

Connect an SSH server, Kubernetes namespace and optional image registry under Integrations. Each service can select its own infrastructure connection. The plan pins that destination, its trust settings and the source commit before you approve it.

For SSH, you need a Linux Docker daemon, an existing private key and the server's verified public host key. The SSH user needs access to the Docker Unix socket and TCP forwarding for readiness checks. Loomplane does not install Docker or use sudo. Choose a published host port for a public HTTP service; private services use the environment's Docker network. A readiness path checks the application over SSH before its binding changes.

For Kubernetes, use a self-contained HTTPS kubeconfig with an embedded CA and token or client certificate. File references, exec authentication, insecure TLS and impersonation are rejected. Select a namespace; Loomplane pins its UID and checks permissions for workloads, services and secrets. Public HTTP services need your ingress controller, hostname and an existing TLS secret. CPU autoscaling also needs the metrics API.

A Kubernetes connection can build and host images in its own namespace. Set `registry_hostname` under Integrations and save. With cluster-admin permissions, Loomplane installs missing Traefik and cert-manager components in `loomplane-system`, detects a supported Linux architecture, and creates a retained copy of the default dynamic storage class. It reuses existing controllers and leaves the original storage class unchanged. Setup runs when you save, never during a health check. Helm is not required at runtime. Leave `build.builder` and `build.registry` empty on services that use this setup. The first approved deployment provisions a registry Deployment, Service, 20 GiB PVC, generated credentials, Ingress and certificate. You don't need an SSH builder or a separate registry connection.

Automatic setup creates a missing application namespace. You can reuse your existing provisioner and load balancer, or select a bare-cluster setup:

| Setting | What you get | What you supply |
| --- | --- | --- |
| `registry_storage=existing` (default) | A retained copy of your dynamic storage class | An installed provisioner; a class name if defaults are ambiguous |
| `registry_storage=local-path` | A dedicated local-path provisioner and retained storage | A Linux node with disk space. Data does not survive loss of that node |
| `registry_storage=longhorn` | Longhorn with three storage replicas and retained volumes | Kubernetes 1.34+, three schedulable Linux nodes, and [Longhorn host prerequisites](https://longhorn.io/docs/1.13.0/deploy/install/) |
| `registry_network=load-balancer` (default) | An ingress LoadBalancer Service | Your cloud's load-balancer controller. Cloud charges may apply |
| `registry_network=node` | Ingress on a node's ports 80 and 443 | `ingress_node` when several nodes qualify, plus firewall/NAT access to that node |
| `registry_network=metallb` | MetalLB with an L2 address pool | An unused LAN range in `load_balancer_addresses`, as CIDRs or start-end ranges |

Longhorn setup waits for its CSI driver and three storage nodes with healthy disks and host prerequisite checks. You can see missing packages, kernel modules and mount requirements in setup status. MetalLB uses a dedicated load-balancer class so it can coexist with another provider. Upgrading an older classless installation briefly pauses ingress while the previous provider releases the Service.

The hostname alone is enough when the cluster already has suitable storage, public routing and a verified organization DNS zone connected to Loomplane. Bare clusters need the choices above because Loomplane cannot infer unused LAN addresses or install host packages. Recheck while controllers or an address are pending. `registry_setup=existing` skips installation. Connections with all three original placement settings keep that behavior unless you choose `automatic`. Retries repair missing owned resources without taking over foreign installations.

DNS automation selects the most specific verified organization zone that contains your registry hostname. It publishes an A, AAAA or CNAME for the discovered ingress address and updates that record when the address changes. The zone's public TXT proof must still be present. Conflicting records are refused, and only the saved owned record can be removed. Use `registry_dns=manual` to manage DNS yourself.

Open **Registry operations** on the connection for status, backups and maintenance. Automatic maintenance checks compatible bundled controller updates daily, rotates credentials every 30 days, backs up daily and collects unreferenced blobs weekly. It starts after the first deployment creates the registry. `registry_maintenance=manual` disables the schedule, leaving the same actions available on demand. Upgrades use versions shipped with Loomplane; major and minor migrations require review.

Backups use your existing Loomplane backup storage configuration. Backup, restore and cleanup pause registry access while a helper mounts the image volume. Builds wait for maintenance, and a restarted server resumes the saved operation. Restore verifies the stored checksum and archive paths before replacing files, and first saves the current data. Backups remain available until you remove them from backup storage. If a credential update interrupts an operation, **Review and retry current operation** renews its authority without losing its checkpoint.

Removal refuses active registry consumers. Image storage is retained by default; deleting it requires a fresh backup. You can also request removal of unused managed controllers, but retained volumes, other cluster connections, ingress routes or controller resources block that cleanup. Namespaces and foreign installations remain. Removing a connection alone does not uninstall shared controllers.

Each build runs the approved source archive in a temporary rootless BuildKit Job, publishes the image and records its digest. Build jobs have a 30-minute deadline, no service account token or host socket, and a one-hour finished-job TTL as a cleanup fallback. Rootless BuildKit needs user namespaces and permits unconfined seccomp and AppArmor profiles inside its build container. A cluster enforcing Restricted Pod Security needs a dedicated namespace with a compatible policy. It never falls back to privileged Docker.

Loomplane creates a workload-owned image pull Secret for each managed-registry consumer. Rotation keeps old and new credentials valid for ten minutes while those copies are refreshed. Rollback restores the current credential with the previous workload. The registry's Basic credentials grant access to the whole registry, so use separate namespaces and connections when workloads must not share image access. Service removal deletes its pull Secret but retains shared image storage. Cleanup removes unreferenced blobs; tagged images and digest-pinned manifests stay available for deployments and rollbacks.

The registry hostname must resolve from build pods and cluster nodes, and its certificate must be trusted by both. cert-manager uses the existing HTTP-01 issuer; DNS must point at ingress before deployment. The installed Traefik accepts streamed uploads and redirects HTTP to HTTPS while preserving HTTP-01 challenge routing. Loomplane sets large-upload annotations for ingress-nginx. Configure equivalent upload limits and HTTPS enforcement when supplying another ingress controller. The registry uses a single replica and a retained volume, so availability and recovery depend on your storage driver.

You can still select an SSH/Docker builder and an OCI registry reachable by both builder and cluster. You can also provide an existing image pinned as `registry/repository@sha256:...`.

For private images from an external registry, enter an existing namespace Secret name in the connection's `image_pull_secret` setting. You manage its credentials and rotation. Loomplane checks its name and registry Secret type through a Table response with `includeObject=None`, then references it in the Pod. Rollback and removal preserve that external Secret. Names beginning with `lp-` are reserved for Loomplane's own objects.

Docker publishing and server pulls accept a direct registry bearer token or a repository token obtained through your reviewed token service. Username/password connections need a Bearer token exchange for authenticated Docker operations. Basic-only credentials can check registry API access, but cannot be delegated to Docker. Kubernetes uses your existing pull Secret for native authentication; it cannot consume a direct bearer token from a generic Docker configuration Secret. See [Docker's registry token contract](https://github.com/moby/moby/blob/v28.0.0/api/types/registry/authconfig.go) and [Kubernetes image authentication](https://kubernetes.io/docs/concepts/containers/images/#specifying-imagepullsecrets-on-a-pod).

The control plane sends registry credentials only to the selected registry or its approved token endpoint. If you use a private CA, install its trust on the builder daemon, target daemon and Kubernetes nodes that contact the registry. The connection's CA configures Loomplane's checks and token exchange.

```yaml
version: 1
services:
  api:
    source: go:cmd/api
    provider: kubernetes
    target: conn_cluster
    port: 8080
    health_path: /_/health
    public: false
    scale: {min: 2, max: 2}
    build:
      context: .
      builder: conn_builder
      registry: conn_registry
  worker:
    source: go:cmd/worker
    provider: server
    target: conn_server
    public: false
    build: {context: .}
```

Replace those connection IDs with the ones you saved. Forge HTTP apps without a Dockerfile can use the generated Go build; a repository Dockerfile takes precedence. Forge's declared worker role keeps a worker from becoming an HTTP service. A private dependency must have a supported network path between its selected targets; choosing destinations does not create a cross-cluster network.

## DNS and domains

Three providers can hold your DNS for the [domains](domains.md) you bring or register. Cloudflare also provides an optional global CDN and edge cache for managed service hostnames. Your service still runs on its existing deployment target:

| Provider | DNS | Registration | Compute |
| --- | --- | --- | --- |
| Cloudflare | Zones and records, wildcard certificate challenges, and optional CDN/cache rules for exact service hostnames | Through Cloudflare Registrar, in your own account | None |
| DigitalOcean | Zones and records | None | App Platform and Managed Databases |
| Vercel | Zones and records | None | Vercel |

## Build output

| Target | Collection |
| --- | --- |
| SSH/Docker and Kubernetes source builds | Docker/BuildKit output from the selected SSH builder |
| Managed Kubernetes source builds | BuildKit output from the cluster build Job |
| Vercel | Streamed deployment build events |
| Cloud Run | Polled Cloud Build entries from Cloud Logging |
| ECS on Fargate | Polled CodeBuild output from its exact CloudWatch log stream |
| Render | Progress only; its published log API cannot isolate one concurrent deployment |
| DigitalOcean App Platform | Progress only; build logs stay on the app's Activity tab |
| Azure Container Apps | Progress only; build logs stay under Runs in the container registry |
| Provider plugin | Optional operation logs through the SDK |

Log access is independent of deployment access. A denied or interrupted collector shows unavailable or incomplete output while the deployment keeps its own status. Logs retain source, action and attempt IDs; retries preserve earlier output. Repeated provider records use stable IDs so reconnects do not duplicate them.

To opt a Go plugin in, implement `sdk.OperationLogger` with `OperationLogs() bool` returning true and call `job.Log(stream, message)`. The handler serves bounded pages at `/v1/operations/{id}/logs`. Older plugins can keep their progress callbacks. Plugin restart or retention loss produces a gap rather than pretending older lines are complete.

A plugin is a small HTTP service. Loomplane reads its description when it starts and puts it in the catalog beside the built-in targets, so manifests can name it, plans check against what it says it runs, and deployments, retries and drift checks drive it like any other. It runs on your infrastructure, so the credential it deploys with never leaves it, apart from the copy Loomplane keeps in Vault.

## A provider in Go

`pkg/provider/sdk` is the Go side of the protocol. You implement five methods and hand the result to `sdk.Handler`:

```go
type Provider interface {
	Describe() provider.Provider
	Check(ctx context.Context, c sdk.Credential) (*sdk.Account, error)
	Apply(ctx context.Context, job *sdk.Job) (*sdk.Outcome, error)
	Remove(ctx context.Context, job *sdk.Job) error
	Observe(ctx context.Context, t *sdk.Target) (*sdk.Observation, error)
}
```

```go
http.ListenAndServe(":9100", sdk.Handler(myProvider, os.Getenv("PLUGIN_TOKEN")))
```

`examples/provider-plugin` is a working one, a static site host in about 270 lines. Read it first; it's the shortest path to a provider that works.

`Describe` returns the catalog entry: a name manifests use (`provider: static-host`), what it runs (`Runs`, by workload: `http-service`, `web-app`, `static-site`, `worker`), what it provisions (`Provisions`, by resource kind: `postgres`, `redis`), its regions, and the `Cloud` its connections are for. Implement `sdk.Connector` too and the dashboard asks for a credential in your words, with the settings you list.

`Check` decides whether a credential works. You get what someone pasted under Integrations and the settings they chose. Return the account it acts as, details safe to show, and anything it's missing. A credential the cloud refused is an error wrapping `sdk.ErrRejected`; Loomplane tells the person and never stores it. Any other error means the cloud couldn't be asked, and they can try again.

`Apply` makes a service or resource exist as the job describes. A service's job has its source (`job.Source()`, the tarball of the service's repository at its commit, with one top-level directory, and that commit as `job.Commit`), the path of its entrypoint in the repository, its Dockerfile if it has one with the directory it builds from (`Context`, from the repository's root), its port, whether it's public, its size and scale, and its variables, with the secret ones listed so you can put them in your cloud's secret store. A resource's job has its kind and version, and no commit. Nor has a gateway's: its source is the program Loomplane generates for its routes. Return an `ExternalID` you'll recognise later, a `URL` for a public service, a `Revision` that drift checks compare with, and for a resource, an `Address` (a database URL, say). Loomplane keeps the address in Vault and hands it, as a secret, to the services that need the resource.

`Remove` deletes what a binding names, with the credential of the connection that deployed it, even when the environment deploys with another one now. Something already gone isn't an error. You'll also get a `Remove` for what a service or resource ran as before it moved to another provider, region or connection, once the new one is up, with the credential of the connection that deployed it. If that one fails, later deployments of the action send it again, so it can arrive long after the move.

`Observe` reads what's live, without changing anything. Return `Exists: false` when it's gone, and the live `Revision`. The drift check calls it on a schedule.

## Running it

```yaml
# Loomplane's config.yaml
providers:
  plugins:
    - url: http://static-host:9100
      token: "${STATIC_HOST_PLUGIN_TOKEN}"
```

The token is a shared secret between Loomplane and the plugin; `sdk.Handler` refuses any request without it. Loomplane describes every plugin when it starts and won't start if one doesn't answer or speaks another protocol version, because it would otherwise accept plans it can't carry out.

Then connect the plugin's cloud under Integrations, and use it in `loomplane.yaml`:

```yaml
services:
  docs:
    source: apps/docs
    provider: static-host
```

## Rules a provider has to follow

Apply, Remove and Observe must be idempotent. A failed deployment can be retried from the step that failed, Loomplane can restart in the middle of one, and a plugin that restarts loses its operations, so the same job can arrive more than once. `job.Key` is the same for every attempt at the same action; use it, or names derived from the job, so a second attempt finds what the first one made and converges on it.

Long work should checkpoint. `job.Checkpoint(data)` saves something a retry gets back as `job.Resume`: a build's ID, an operation name. `job.Progress(message)` shows on the deployment's page what the step is doing.

Never run the repository's code on the plugin's machine unless that machine is meant for it. Build on your cloud's builder, the way the built-in targets use Cloud Build and CodeBuild.

## The protocol

You don't need Go. A plugin in any language that answers these, as JSON, with `Authorization: Bearer <token>` on every request, is a provider:

| Request | Answer |
|---|---|
| `GET /v1/describe` | `{"protocol": "loomplane.provider/v1", "provider": {...}, "connection": {...}}` |
| `POST /v1/check` with `{"secret": base64, "settings": {...}}` | `{"account", "details", "missing"}`, or 422 `{"error", "rejected": true}` |
| `HEAD /v1/sources/{id}` | 200 if you have that source, 404 if not |
| `PUT /v1/sources/{id}` with the gzipped tarball | 204 |
| `POST /v1/operations` with `{"kind": "apply" or "remove", "job": {...}, "source": id}` | 202 and the operation |
| `GET /v1/operations/{id}` | `{"id", "status": "running", "succeeded" or "failed", "progress", "checkpoint", "outcome", "error"}` |
| `POST /v1/observe` with a target | `{"exists", "revision"}` |

Apply and Remove run as operations because builds and databases take minutes. Loomplane starts one, then reads it until it's done. Starting the same job while it's still running should return the running operation. An operation you don't have any more is a 404, and Loomplane starts it again from the last checkpoint. `pkg/provider/sdk/protocol.go` has every type.

## Testing one

`internal/plugins` has the client Loomplane uses, and its tests run it against `sdk.Handler` with a fake provider: that's the quickest check that a provider speaks the protocol. `tests/integration/plugin_test.go` goes further, connecting the example's cloud, then planning, deploying, re-planning and removing a site through a running Loomplane.

### Kubernetes cluster lifecycle

DigitalOcean exposes cluster discovery and live cluster options separately from creation authority. Organization managers can review cluster setup in Clouds, Infrastructure, Clusters or through `loomctl cloud-clusters`. Project grants expose runtime targets without granting provider lifecycle management.

Managed DOKS creation remains disabled by default and restricted to configured qualification operators. New clusters support private digest-pinned workers with project-owned renewable access. Adoption can verify existing HTTP and TLS prerequisites, but managed namespace storage and builds remain unsupported. Installed adapter capability is not live qualification, and general purchases require the later cleanup qualification gate. See [DigitalOcean](digitalocean.md#managed-kubernetes-worker-clusters) for the current boundaries.
