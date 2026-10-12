# Writing a provider

A provider is a deployment target: somewhere Loomplane can run a service or provision a resource. Loomplane ships with Cloud Run, Vercel, ECS on Fargate, Cloud SQL and RDS. You can add your own without changing Loomplane: write a provider plugin, run it, and point Loomplane at it.

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

`Apply` makes a service or resource exist as the job describes. A service's job has its source (`job.Source()`, the commit's tarball with one top-level directory), the path of its entrypoint in the repository, its Dockerfile if it has one, its port, whether it's public, its size and scale, and its variables, with the secret ones listed so you can put them in your cloud's secret store. A resource's job has its kind and version. Return an `ExternalID` you'll recognise later, a `URL` for a public service, a `Revision` that drift checks compare with, and for a resource, an `Address` (a database URL, say). Loomplane keeps the address in Vault and hands it, as a secret, to the services that need the resource.

`Remove` deletes what a binding names. Something already gone isn't an error.

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
