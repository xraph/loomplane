# Cloudflare resources and Workers

Use **Resources** to create a D1 database, KV namespace, R2 bucket, Queue, Vectorize index or Hyperdrive connection. Creation goes through your manifest and deployment review. Sync an existing account to discover its resources, then choose **Use native binding** or save separate application credentials before attaching one.

| Resource | Cloudflare Worker | Other running applications |
| --- | --- | --- |
| D1, KV, Queues, Vectorize | Native binding | Supported Cloudflare API with an application token |
| R2 | Native binding | S3 application keys and the account's R2 endpoint |
| Hyperdrive | Native binding | Requires a Worker |
| Durable Objects | Exported Worker class or existing namespace | Requires a Worker |

A resource can be freshly observed even when another product's permission check fails. An incomplete sync does not prove an unseen resource exists. Recreated R2 buckets and Vectorize indexes must match their recorded creation identity before a Worker can use them.

## Deploy a Worker

Use a Cloudflare API-token connection with Workers Scripts access and the permissions required by your resources. R2-only OAuth consent does not grant Worker deployment access. Application credentials saved for a resource are separate from the account credential used to manage it.

Commit the Worker module you want deployed. Loomplane uploads `.js` and `.mjs` modules from your reviewed commit, including relative module imports. Bundle TypeScript and package dependencies before committing the output. Build scripts and dependency installation do not run in the control plane.

```yaml
version: 1
resources:
  db:
    kind: cloudflare-d1
    provider: cloudflare-workers
  assets:
    kind: object-storage
    provider: cloudflare-workers
    settings:
      jurisdiction: default
services:
  app:
    source: .
    kind: http-service
    provider: cloudflare-workers
    worker:
      main: worker.js
      compatibility_date: "2026-11-01"
      durable_objects:
        - name: COUNTER
          class_name: Counter
    needs: [db, assets]
```

The Worker receives `env.DB`, `env.ASSETS` and `env.COUNTER`. Native bindings require the same reviewed Cloudflare account. Use `target` to select a particular checked connection when you have several accounts. Each service and environment gets its own Worker identity.

Durable Object creation belongs to the exported class lifecycle. Declare your classes under `worker.durable_objects`; do not create a standalone namespace through the generic resource form. Existing namespaces can be synced and attached. Removing a Worker that still owns Durable Object data is blocked, and disconnecting an imported binding never deletes that data.

Hyperdrive creation requires `origin_endpoint`, a verified PostgreSQL or MySQL application endpoint, and `ca_certificate_id`, an existing Cloudflare CA certificate. Endpoint permissions, revision and TLS identity are rechecked before credentials are read or sent.

See [the committed Worker example](../examples/cloudflare-worker/worker.js) and [Cloudflare's bindings guide](https://developers.cloudflare.com/workers/runtime-apis/bindings/).
