# Writing a provider

A provider is a deployment target: somewhere Loomplane can run a service or provision a resource. Loomplane ships with Cloud Run, Vercel, ECS on Fargate, Render, DigitalOcean App Platform, Azure Container Apps, SSH/Docker servers and Kubernetes for services, and Cloud SQL, RDS, Memorystore, ElastiCache, Cloud Storage, S3 and DigitalOcean Managed Databases for resources. The [DigitalOcean guide](digitalocean.md) covers how App Platform deploys an approved commit when it can only build a branch, and the [Azure guide](azure.md) covers the service principal Container Apps deploys with. You can add your own without changing Loomplane: write a provider plugin, run it, and point Loomplane at it.

## Your servers and clusters

Connect an SSH server, Kubernetes namespace and optional image registry under Integrations. Each service can select its own infrastructure connection. The plan pins that destination, its trust settings and the source commit before you approve it.

For SSH, you need a Linux Docker daemon, an existing private key and the server's verified public host key. The SSH user needs access to the Docker Unix socket and TCP forwarding for readiness checks. Loomplane does not install Docker or use sudo. Choose a published host port for a public HTTP service; private services use the environment's Docker network. A readiness path checks the application over SSH before its binding changes.

For Kubernetes, use a self-contained HTTPS kubeconfig with an embedded CA and token or client certificate. File references, exec authentication, insecure TLS and impersonation are rejected. Select an existing namespace; Loomplane pins its UID and checks permissions for workloads, services and secrets. Public HTTP services need your ingress controller, hostname and an existing TLS secret. CPU autoscaling also needs the metrics API.

A source build for Kubernetes needs an SSH/Docker builder and an OCI registry reachable by both builder and cluster. You can instead provide an existing image pinned as `registry/repository@sha256:...`.

For private Kubernetes images, enter an existing namespace Secret name in the connection's `image_pull_secret` setting. You manage its credentials and rotation. Loomplane checks its name and registry Secret type through a Table response with `includeObject=None`, then references it in the Pod. Rollback and removal preserve that external Secret. Names beginning with `lp-` are reserved for Loomplane's own objects.

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

## Build output

| Target | Collection |
| --- | --- |
| SSH/Docker and Kubernetes source builds | Docker/BuildKit output from the selected SSH builder |
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
